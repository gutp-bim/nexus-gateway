// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"nexus-gateway/internal/common"
)

// Connector is a simulated protocol connector. It publishes Common Events on
// subject evt.sim.<connectorID> at the configured interval (ADR-0001, ADR-0005).
// It carries native addressing only; it never emits a canonical point_id.
type Connector struct {
	connectorID string
	points      []Point
	interval    time.Duration
	js          jetstream.JetStream
}

// Point is one simulated point definition (native addressing only).
type Point struct {
	LocalID   string
	DeviceRef string
	Unit      string
	// baseValue and amplitude for a simple sine wave
	BaseValue float64
	Amplitude float64
}

// defaultInterval is the 1-minute freshness floor used when no positive interval
// is supplied (a non-positive value would panic time.NewTicker).
const defaultInterval = 60 * time.Second

func New(connectorID string, js jetstream.JetStream, interval time.Duration, points []Point) *Connector {
	if interval <= 0 {
		slog.Warn("sim: non-positive interval, using default", "interval", interval, "default", defaultInterval)
		interval = defaultInterval
	}
	return &Connector{connectorID: connectorID, js: js, interval: interval, points: points}
}

// Run publishes events at the configured interval until ctx is cancelled.
func (c *Connector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			for _, p := range c.points {
				c.publishPoint(ctx, t, p, tick)
			}
			tick++
		}
	}
}

func (c *Connector) publishPoint(ctx context.Context, t time.Time, p Point, tick int) {
	value := p.BaseValue + p.Amplitude*math.Sin(float64(tick)*0.1)
	evt := common.Event{
		Protocol:    "sim",
		ConnectorID: c.connectorID,
		LocalID:     p.LocalID,
		DeviceRef:   p.DeviceRef,
		Value:       value,
		Unit:        p.Unit,
		Quality:     "Good",
		Timestamp:   t.UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		slog.Error("sim: marshal error", "err", err)
		return
	}
	subject := "evt.sim." + c.connectorID
	if _, err := c.js.Publish(ctx, subject, data); err != nil {
		slog.Warn("sim: publish error", "subject", subject, "err", err)
	}
}
