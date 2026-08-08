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

	"nexus-gateway/connector/sdk"
)

func TestMetricsHandler_WritesPrometheusText(t *testing.T) {
	rec := httptest.NewRecorder()
	sdk.MetricsHandler(func() []sdk.Metric {
		return []sdk.Metric{
			{Name: "mqtt_received_total", Help: "Messages received.", Type: "counter", Value: 305},
		}
	})(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := "# HELP mqtt_received_total Messages received.\n# TYPE mqtt_received_total counter\nmqtt_received_total 305\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}

// A connector with no instrumentation must still answer /metrics rather than
// 404 or panic, so a scrape config can target every connector uniformly.
func TestMetricsHandler_NilCollectServesEmptyBody(t *testing.T) {
	rec := httptest.NewRecorder()
	sdk.MetricsHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("got %d %q, want 200 with an empty body", rec.Code, rec.Body.String())
	}
}

// /metrics rides on the health server's port: one connector surface, two routes.
func TestHealthServer_ServesMetricsOnTheSamePort(t *testing.T) {
	srv := sdk.StartHealthServer("127.0.0.1:0", func() bool { return true }, func() []sdk.Metric {
		return []sdk.Metric{{Name: "mqtt_published_total", Help: "Events published.", Type: "counter", Value: 7}}
	})
	if srv == nil {
		t.Fatal("StartHealthServer returned nil")
	}
	defer srv.Shutdown(context.Background())

	resp, err := http.Get("http://" + srv.Addr() + "/metrics") //nolint:noctx // short-lived test request
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "mqtt_published_total 7") {
		t.Fatalf("got %d %q, want 200 with the counter series", resp.StatusCode, string(b))
	}
}
