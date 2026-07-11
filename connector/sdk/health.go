// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// HealthHandler serves the connector health contract (connector-spec §5.5): a
// GET /health that returns 200 {"status":"ok"} when the protocol session is
// established (ready() true) and 503 {"status":"degraded"} otherwise. The
// gateway health monitor greps for the literal "status":"ok", so the degraded
// body deliberately does not contain it.
func HealthHandler(ready func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
	}
}

// HealthServer is a minimal HTTP server exposing GET /health.
type HealthServer struct {
	srv  *http.Server
	addr string
}

// StartHealthServer starts a /health server on addr (e.g. ":8080"), reporting
// the ready probe. A bind failure is logged and returns nil (non-fatal — the
// connector keeps running without a health surface rather than crashing).
func StartHealthServer(addr string, ready func() bool) *HealthServer {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", HealthHandler(ready))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("health: listen failed — /health disabled", "addr", addr, "err", err)
		return nil
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health: server error", "err", err)
		}
	}()
	slog.Info("health: serving /health", "addr", ln.Addr().String())
	return &HealthServer{srv: srv, addr: ln.Addr().String()}
}

// Addr is the resolved listen address (useful when addr used port 0).
func (h *HealthServer) Addr() string {
	if h == nil {
		return ""
	}
	return h.addr
}

// Shutdown gracefully stops the health server. Safe on a nil receiver.
func (h *HealthServer) Shutdown(ctx context.Context) {
	if h == nil || h.srv == nil {
		return
	}
	_ = h.srv.Shutdown(ctx)
}

// AwaitStream blocks until the named JetStream stream exists or ctx is cancelled.
// The gateway owns the EVENTS stream (connector-spec §2.1); a connector must wait
// for it rather than publishing into a void and spamming errors on every attempt.
// It polls every pollInterval, logging once per poll.
func AwaitStream(ctx context.Context, js jetstream.JetStream, name string, pollInterval time.Duration) error {
	for {
		if _, err := js.Stream(ctx, name); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Info("connector: JetStream stream not ready — waiting (start the gateway)",
			"stream", name, "poll", pollInterval)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
