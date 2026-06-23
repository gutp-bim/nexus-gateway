// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"nexus-gateway/connector/sim"
)

func main() {
	natsURL := flag.String("nats", envOrDefault("NATS_URL", nats.DefaultURL), "NATS URL")
	connID := flag.String("connector-id", envOrDefault("CONNECTOR_ID", "sim-01"), "Connector ID")
	// 1-minute freshness floor by default, consistent with the BACnet/OPC-UA
	// connectors; override with --interval or SIM_POLL_INTERVAL (e.g. "5s").
	interval := flag.Duration("interval", envDurationOrDefault("SIM_POLL_INTERVAL", 60*time.Second), "Publish interval")
	flag.Parse()

	nc, err := nats.Connect(*natsURL,
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connector := sim.New(*connID, js, *interval, []sim.Point{
		{LocalID: "sim://ahu-01/supply_air_temp", DeviceRef: "sim://ahu-01", Unit: "Cel", BaseValue: 22.0, Amplitude: 3.0},
		{LocalID: "sim://ahu-01/fan_run", DeviceRef: "sim://ahu-01", Unit: "", BaseValue: 1.0, Amplitude: 0.0},
	})
	go connector.Run(ctx)

	slog.Info("sim-connector started", "connector_id", *connID, "nats", *natsURL)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	slog.Info("sim-connector shutting down")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDurationOrDefault parses a Go duration (e.g. "60s", "500ms") from the env
// var, falling back to def when unset or unparseable.
func envDurationOrDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		slog.Warn("invalid duration in env, using default", "key", key, "value", v, "default", def)
	}
	return def
}
