// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	mqttconn "nexus-gateway/connector/mqtt"
	"nexus-gateway/connector/sdk"
)

// pointEnv is the JSON schema for one entry in MQTT_POINTS (snake_case for shell-friendliness).
type pointEnv struct {
	Topic           string `json:"topic"`
	DeviceRef       string `json:"device_ref"`
	Unit            string `json:"unit"`
	Writable        bool   `json:"writable"`
	CommandTopic    string `json:"command_topic"`
	PayloadTemplate string `json:"payload_template"`
}

func main() {
	// Register the signal handler before starting goroutines so a SIGTERM that
	// arrives during the startup window (including the EVENTS stream wait below)
	// is captured, not handled by Go's default handler (which exits immediately,
	// skipping deferred cleanup). Cancelling sigCtx unblocks AwaitStream too.
	sigCtx, stopNotify := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopNotify()

	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	connID := envOrDefault("CONNECTOR_ID", "mqtt-01")
	brokerURL := envOrDefault("MQTT_BROKER_URL", "mqtt://localhost:1883")
	clientID := envOrDefault("MQTT_CLIENT_ID", connID)
	username := os.Getenv("MQTT_USERNAME")
	password := []byte(os.Getenv("MQTT_PASSWORD"))
	if len(password) == 0 {
		password = nil
	}

	// Reject values that would silently truncate to an unintended uint16:
	// e.g. MQTT_KEEPALIVE=65536 → 0, disabling keepalive entirely.
	keepAliveRaw := envUint("MQTT_KEEPALIVE", 30)
	if keepAliveRaw > math.MaxUint16 {
		slog.Error("MQTT_KEEPALIVE exceeds maximum allowed value (65535)", "value", keepAliveRaw)
		os.Exit(1)
	}
	keepAlive := uint16(keepAliveRaw)
	sessionExpiry := uint32(envUint("MQTT_SESSION_EXPIRY", 0))

	// TLS material for mqtts:// brokers (#33). Consulted only for TLS schemes.
	tlsCAFile := os.Getenv("MQTT_TLS_CA_FILE")
	tlsCertFile := os.Getenv("MQTT_TLS_CERT_FILE")
	tlsKeyFile := os.Getenv("MQTT_TLS_KEY_FILE")
	tlsInsecure := isTruthy(os.Getenv("MQTT_TLS_INSECURE_SKIP_VERIFY"))
	if tlsInsecure {
		slog.Warn("MQTT_TLS_INSECURE_SKIP_VERIFY is set — broker certificate verification is DISABLED (dev only)")
	}

	// Freshness floor (#34): re-publish last-known values at this cadence so a
	// never-changing Point matches the BACnet/OPC-UA poll cadence. Default 60s;
	// set MQTT_FRESHNESS_INTERVAL=0 to disable (pure push).
	freshness, err := parseDurationDefault("MQTT_FRESHNESS_INTERVAL", 60*time.Second)
	if err != nil {
		slog.Error("MQTT_FRESHNESS_INTERVAL: invalid duration", "err", err)
		os.Exit(1)
	}

	var envPoints []pointEnv
	if raw := envOrDefault("MQTT_POINTS", "[]"); raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &envPoints); err != nil {
			slog.Error("MQTT_POINTS: invalid JSON", "err", err)
			os.Exit(1)
		}
	}

	// Validate every point at startup to surface misconfiguration immediately
	// rather than silently misbehaving at runtime.
	for i, p := range envPoints {
		if p.Topic == "" {
			slog.Error("MQTT_POINTS: topic must not be empty", "index", i)
			os.Exit(1)
		}
		if p.Writable && p.CommandTopic == "" {
			slog.Error("MQTT_POINTS: writable point requires command_topic", "index", i, "topic", p.Topic)
			os.Exit(1)
		}
	}

	points := make([]mqttconn.PointConfig, len(envPoints))
	for i, p := range envPoints {
		points[i] = mqttconn.PointConfig{
			Topic:           p.Topic,
			DeviceRef:       p.DeviceRef,
			Unit:            p.Unit,
			Writable:        p.Writable,
			CommandTopic:    p.CommandTopic,
			PayloadTemplate: p.PayloadTemplate,
		}
	}

	nc, err := nats.Connect(natsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		slog.Error("NATS connect failed", "err", err)
		os.Exit(1)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("JetStream init failed", "err", err)
		nc.Close()
		os.Exit(1)
	}

	cfg := mqttconn.Config{
		ConnectorID:           connID,
		BrokerURL:             brokerURL,
		ClientID:              clientID,
		Username:              username,
		Password:              password,
		KeepAlive:             keepAlive,
		SessionExpiry:         sessionExpiry,
		Points:                points,
		TLSCAFile:             tlsCAFile,
		TLSCertFile:           tlsCertFile,
		TLSKeyFile:            tlsKeyFile,
		TLSInsecureSkipVerify: tlsInsecure,
		FreshnessInterval:     freshness,
	}

	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	connector := mqttconn.New(cfg, nc, js)

	// Health endpoint (#35): /health reports ok only while the broker session is up.
	healthAddr := ":" + envOrDefault("HEALTH_PORT", "8080")
	health := sdk.StartHealthServer(healthAddr, connector.Healthy)

	// Wait for the gateway-owned EVENTS stream before publishing (#35) so early
	// Common Events are not dropped into a missing stream. Interruptible via signal.
	if err := sdk.AwaitStream(ctx, js, "EVENTS", 5*time.Second); err != nil {
		slog.Info("mqtt-connector: shutting down before the EVENTS stream was ready")
		health.Shutdown(context.Background())
		nc.Close()
		return
	}

	// Track the Run goroutine so an unexpected exit (e.g. broker URL parse
	// error, NATS subscribe failure) causes the process to exit rather than
	// silently becoming a zombie that Docker never restarts.
	done := make(chan struct{})
	go func() {
		connector.Run(ctx)
		close(done)
	}()

	slog.Info("mqtt-connector started", "connector_id", connID, "nats", natsURL, "broker", brokerURL, "points", len(points))

	select {
	case <-ctx.Done():
		slog.Info("mqtt-connector shutting down")
	case <-done:
		slog.Error("mqtt-connector Run exited unexpectedly")
		health.Shutdown(context.Background())
		cancel()
		nc.Close()
		os.Exit(1)
	}

	// Cancel the context first so autopaho disconnects cleanly and the Run
	// goroutine drains any in-flight PUBACK acknowledgements.  Only then close
	// the NATS connection — closing it before Run returns would cut off
	// pending JetStream publishes and trigger broker redeliver (double-count).
	cancel()
	<-done
	nc.Close()
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envUint(key string, def uint64) uint64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
		slog.Warn("invalid uint in env, using default", "key", key, "default", def)
	}
	return def
}

// parseDurationDefault reads a Go duration string (e.g. "60s", "0") from env,
// returning def when unset and an error when set but unparseable.
func parseDurationDefault(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	return time.ParseDuration(v)
}

// isTruthy reports whether an env value represents an enabled boolean flag.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
