// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	mqttconn "nexus-gateway/connector/mqtt"
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
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	connID := envOrDefault("CONNECTOR_ID", "mqtt-01")
	brokerURL := envOrDefault("MQTT_BROKER_URL", "mqtt://localhost:1883")
	clientID := envOrDefault("MQTT_CLIENT_ID", connID)
	username := os.Getenv("MQTT_USERNAME")
	password := []byte(os.Getenv("MQTT_PASSWORD"))
	if len(password) == 0 {
		password = nil
	}
	keepAlive := uint16(envUint("MQTT_KEEPALIVE", 30))
	sessionExpiry := uint32(envUint("MQTT_SESSION_EXPIRY", 0))

	var envPoints []pointEnv
	if raw := envOrDefault("MQTT_POINTS", "[]"); raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &envPoints); err != nil {
			slog.Error("MQTT_POINTS: invalid JSON", "err", err)
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
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("JetStream init failed", "err", err)
		os.Exit(1)
	}

	cfg := mqttconn.Config{
		ConnectorID:   connID,
		BrokerURL:     brokerURL,
		ClientID:      clientID,
		Username:      username,
		Password:      password,
		KeepAlive:     keepAlive,
		SessionExpiry: sessionExpiry,
		Points:        points,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connector := mqttconn.New(cfg, nc, js)
	go connector.Run(ctx)

	slog.Info("mqtt-connector started", "connector_id", connID, "nats", natsURL, "broker", brokerURL, "points", len(points))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	slog.Info("mqtt-connector shutting down")
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
