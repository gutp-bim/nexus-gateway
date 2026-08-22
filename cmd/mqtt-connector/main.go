// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	// QoS/CommandQoS are optional per-point overrides (#119, docs/adr/0008);
	// omitted (0) falls back to the connector's implicit QoS 1 default.
	QoS        byte `json:"qos"`
	CommandQoS byte `json:"command_qos"`
}

type subscriptionEnv struct {
	Filter string `json:"filter"`
	QoS    byte   `json:"qos"`
}

const (
	defaultMaxPayloadBytes uint64 = 1024
	// defaultReceiveMaximum mirrors the connector package default; it is repeated
	// here only so the env fallback is visible next to the other MQTT_* defaults.
	defaultReceiveMaximum uint64 = 1024
)

func main() {
	// Register signal handler before starting goroutines so a SIGTERM that
	// arrives during the startup window is captured, not handled by Go's
	// default handler (which exits immediately, skipping deferred cleanup).
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
	receiveMaximumRaw := envUint("MQTT_RECEIVE_MAXIMUM", defaultReceiveMaximum)
	if receiveMaximumRaw == 0 || receiveMaximumRaw > math.MaxUint16 {
		slog.Error("MQTT_RECEIVE_MAXIMUM must be between 1 and 65535", "value", receiveMaximumRaw)
		os.Exit(1)
	}
	receiveMaximum := uint16(receiveMaximumRaw)
	sessionExpiry := uint32(envUint("MQTT_SESSION_EXPIRY", 0))
	freshness, err := parseDurationDefault("MQTT_FRESHNESS_INTERVAL", 60*time.Second)
	if err != nil {
		slog.Error("MQTT_FRESHNESS_INTERVAL: invalid duration", "err", err)
		os.Exit(1)
	}
	maxPayloadBytes := envUint("MQTT_MAX_PAYLOAD_BYTES", defaultMaxPayloadBytes)
	if maxPayloadBytes == 0 {
		slog.Error("MQTT_MAX_PAYLOAD_BYTES must be greater than zero")
		os.Exit(1)
	}

	envPoints, err := loadPointEnv(envOrDefault("MQTT_POINTS", "[]"), os.Getenv("MQTT_POINTS_FILE"))
	if err != nil {
		slog.Error("MQTT points configuration invalid", "err", err)
		os.Exit(1)
	}
	subscriptions, err := parseSubscriptionEnv(envOrDefault("MQTT_SUBSCRIPTIONS", "[]"))
	if err != nil {
		slog.Error("MQTT_SUBSCRIPTIONS: invalid configuration", "err", err)
		os.Exit(1)
	}
	ignoreTopics, err := parseStringListEnv("MQTT_IGNORE_TOPICS", envOrDefault("MQTT_IGNORE_TOPICS", "[]"))
	if err != nil {
		slog.Error("MQTT_IGNORE_TOPICS: invalid configuration", "err", err)
		os.Exit(1)
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
			QoS:             p.QoS,
			CommandQoS:      p.CommandQoS,
		}
	}

	// Validate every point at startup to surface misconfiguration immediately
	// rather than silently misbehaving at runtime. Shared with internal/mqttsync's
	// pre-apply validation (#119, docs/adr/0008).
	if err := mqttconn.ValidatePoints(points); err != nil {
		slog.Error("MQTT points configuration invalid", "err", err)
		os.Exit(1)
	}

	tlsConfig, err := mqttconn.LoadTLSConfig(
		envAlias("MQTT_TLS_CA_FILE", "MQTT_CA_FILE"),
		envAlias("MQTT_TLS_CERT_FILE", "MQTT_CERT_FILE"),
		envAlias("MQTT_TLS_KEY_FILE", "MQTT_KEY_FILE"),
	)
	if err != nil {
		slog.Error("MQTT TLS configuration invalid", "err", err)
		os.Exit(1)
	}
	if isTruthy(os.Getenv("MQTT_TLS_INSECURE_SKIP_VERIFY")) {
		slog.Warn("MQTT_TLS_INSECURE_SKIP_VERIFY is set — broker certificate verification is DISABLED (dev only)")
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // explicit, documented development-only opt-in
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
		ConnectorID:       connID,
		BrokerURL:         brokerURL,
		ClientID:          clientID,
		Username:          username,
		Password:          password,
		KeepAlive:         keepAlive,
		SessionExpiry:     sessionExpiry,
		MaxPayloadBytes:   maxPayloadBytes,
		TLSConfig:         tlsConfig,
		Subscriptions:     subscriptions,
		IgnoreTopics:      ignoreTopics,
		Points:            points,
		FreshnessInterval: freshness,
		ReceiveMaximum:    receiveMaximum,
	}

	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	connector := mqttconn.New(cfg, nc, js)
	healthAddr := ":" + envOrDefault("HEALTH_PORT", "8080")
	health := sdk.StartHealthServer(healthAddr, connector.Healthy, connector.Metrics)

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
		cancel()
		nc.Close()
		os.Exit(1)
	}

	// Cancel the context first so autopaho disconnects cleanly and the Run
	// goroutine flushes the messages still queued for JetStream.  Only then close
	// the NATS connection — closing it before Run returns would cut off that
	// flush, and with MQTT_SESSION_EXPIRY=0 the broker would not redeliver.
	cancel()
	<-done
	nc.Close()
}

func envAlias(primary, alias string) string {
	if value := os.Getenv(primary); value != "" {
		return value
	}
	return os.Getenv(alias)
}

func parseStringListEnv(name, raw string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	for i, value := range values {
		if value == "" {
			return nil, fmt.Errorf("%s: item %d must not be empty", name, i)
		}
	}
	return values, nil
}

func loadPointEnv(raw, file string) ([]pointEnv, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read MQTT_POINTS_FILE: %w", err)
		}
		raw = string(data)
	}
	var points []pointEnv
	if err := json.Unmarshal([]byte(raw), &points); err != nil {
		return nil, fmt.Errorf("decode points JSON: %w", err)
	}
	return points, nil
}

func parseSubscriptionEnv(raw string) ([]mqttconn.SubscriptionConfig, error) {
	var configured []subscriptionEnv
	if err := json.Unmarshal([]byte(raw), &configured); err != nil {
		return nil, err
	}
	result := make([]mqttconn.SubscriptionConfig, len(configured))
	for i, sub := range configured {
		if sub.Filter == "" {
			return nil, fmt.Errorf("subscription %d: filter must not be empty", i)
		}
		if sub.QoS > 1 {
			return nil, fmt.Errorf("subscription %d: QoS must be 0 or 1", i)
		}
		result[i] = mqttconn.SubscriptionConfig{Filter: sub.Filter, QoS: sub.QoS}
	}
	return result, nil
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

func parseDurationDefault(key string, def time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return def, nil
	}
	return time.ParseDuration(value)
}

func isTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
