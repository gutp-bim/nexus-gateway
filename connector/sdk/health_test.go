// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package sdk_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nexus-gateway/connector/sdk"
)

func TestHealthHandler_DegradedWhenNotReady(t *testing.T) {
	rec := httptest.NewRecorder()
	sdk.HealthHandler(func() bool { return false })(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("degraded body must not contain the ok marker, got %q", rec.Body.String())
	}
}

func TestHealthHandler_OkWhenReady(t *testing.T) {
	rec := httptest.NewRecorder()
	sdk.HealthHandler(func() bool { return true })(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthy body must contain the ok marker the gateway greps for, got %q", rec.Body.String())
	}
}

// TestHealthServer_ReflectsProbe drives the full server: a flippable probe must
// change the live /health response, and Shutdown must stop it.
func TestHealthServer_ReflectsProbe(t *testing.T) {
	ready := false
	srv := sdk.StartHealthServer("127.0.0.1:0", func() bool { return ready })
	if srv == nil {
		t.Fatal("StartHealthServer returned nil")
	}
	defer srv.Shutdown(context.Background())

	url := "http://" + srv.Addr() + "/health"
	get := func() (int, string) {
		t.Helper()
		resp, err := http.Get(url) //nolint:noctx // short-lived test request
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, body := get(); code != http.StatusServiceUnavailable || strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("not-ready: got %d %q, want 503 without ok marker", code, body)
	}

	ready = true
	if code, body := get(); code != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("ready: got %d %q, want 200 with ok marker", code, body)
	}

	srv.Shutdown(context.Background())
	// After shutdown the endpoint should no longer answer.
	client := http.Client{Timeout: time.Second}
	if resp, err := client.Get(url); err == nil {
		resp.Body.Close()
		t.Fatalf("expected error after shutdown, got status %d", resp.StatusCode)
	}
}
