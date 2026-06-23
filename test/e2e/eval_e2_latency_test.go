// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestE2_EndToEndLatency (research evaluation E2) measures the time from device
// data creation to TelemetryFrame delivery at Building OS, decomposed by pipeline
// stage.
//
// Pipeline stages and measurable breakpoints:
//
//	[Device] → T0: device reading timestamp (embedded in Common Event)
//	          → T1: NATS publish (JetStream sequence arrival time)   ← connector stage
//	          → T2: Normalizer consume + emit TelemetryFrame         ← normalizer stage
//	          → T3: S&F write to SQLite ring buffer                  ← storefwd stage
//	          → T4: gRPC StreamTelemetry send                        ← uplink stage
//	          → T5: Building OS GatewayIngress receive               ← network/BOS stage
//
// This test measures what is observable without code changes:
//
//   - device→NATS gap  : wall clock at consumption minus event.Timestamp
//     (includes connector read cycle + JetStream publish + consumer lag)
//   - storefwd_sent_total delta: frames delivered during window (proxy for T3→T4)
//
// Full T0→T5 breakdown requires a mock Building OS that timestamps receives and
// exposes them via /metrics or a NATS subject; see TODO below.
//
// Metrics reported (p50 / p95 / p99 / max over E2E_E2_SAMPLES samples):
//   - device_to_nats_ms  : T0 → T1 (ms)
//
// Environment:
//
//	E2E_NATS_URL       — required; NATS URL (else test skips)
//	E2E_ADMIN_URL      — gateway Admin API (default http://localhost:8080)
//	E2E_E2_PROTOCOL    — connector protocol to listen on (default "opcua")
//	E2E_E2_CONNECTOR   — connector ID (default "opcua-01")
//	E2E_E2_SAMPLES     — number of events to collect (default 200)
func TestE2_EndToEndLatency(t *testing.T) {
	requireEnv(t, "E2E_NATS_URL")
	adminURL := getenvOr("E2E_ADMIN_URL", "http://localhost:8080")
	protocol := getenvOr("E2E_E2_PROTOCOL", "opcua")
	connID := getenvOr("E2E_E2_CONNECTOR", "opcua-01")
	sampleTarget := parseIntEnv(t, "E2E_E2_SAMPLES", 200)

	_, js := connectNATS(t)
	subject := "evt." + protocol + "." + connID

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		FilterSubject: subject,
		AckPolicy:     jetstream.AckNonePolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Skipf("EVENTS stream not available: %v", err)
	}

	mBefore := scrapeGatewayMetrics(t, adminURL)
	tStart := time.Now()

	var deviceToNATSMs []time.Duration
	collected := 0
	for collected < sampleTarget && ctx.Err() == nil {
		msgs, fetchErr := cons.Fetch(64, jetstream.FetchMaxWait(2*time.Second))
		if fetchErr != nil {
			continue
		}
		tConsumed := time.Now() // wall clock when we receive the message

		for msg := range msgs.Messages() {
			var ev commonEvent
			if json.Unmarshal(msg.Data(), &ev) != nil {
				continue
			}
			deviceTS, tsErr := time.Parse(time.RFC3339Nano, ev.Timestamp)
			if tsErr != nil {
				continue
			}
			// device→NATS gap: wall clock at JetStream consumption minus device read time.
			// Includes: connector read cycle + NATS publish + consumer fetch latency.
			gap := tConsumed.Sub(deviceTS)
			if gap >= 0 && gap < 60*time.Second { // discard stale events
				deviceToNATSMs = append(deviceToNATSMs, gap)
				collected++
			}
		}
	}

	elapsed := time.Since(tStart)
	mAfter := scrapeGatewayMetrics(t, adminURL)
	sentFrames := diffMetric(mBefore, mAfter, "storefwd_sent_total")

	if len(deviceToNATSMs) < 10 {
		t.Skipf("insufficient samples (%d < 10); is %s flowing?", len(deviceToNATSMs), subject)
	}

	p50 := percentileD(deviceToNATSMs, 50)
	p95 := percentileD(deviceToNATSMs, 95)
	p99 := percentileD(deviceToNATSMs, 99)
	pMax := percentileD(deviceToNATSMs, 100)

	t.Logf("E2 device→NATS latency (%d samples, %.0fs window):", len(deviceToNATSMs), elapsed.Seconds())
	t.Logf("  p50=%s  p95=%s  p99=%s  max=%s", p50, p95, p99, pMax)
	t.Logf("  frames delivered to Building OS during window: %.0f", sentFrames)

	// TODO: add T4→T5 (gRPC send → BOS receive) when a mock Building OS ingress
	// exposes receive timestamps via /metrics or NATS rx.<gateway_id> subject.

	// CSV for paper Table 2.
	t.Log("E2 results (CSV — copy into paper Table 2):")
	t.Log("stage,p50_ms,p95_ms,p99_ms,max_ms,samples")
	t.Log(evalCSVRow(
		"device_to_nats",
		fmt.Sprintf("%.1f", float64(p50.Milliseconds())),
		fmt.Sprintf("%.1f", float64(p95.Milliseconds())),
		fmt.Sprintf("%.1f", float64(p99.Milliseconds())),
		fmt.Sprintf("%.1f", float64(pMax.Milliseconds())),
		len(deviceToNATSMs),
	))
	t.Log(evalCSVRow(
		"normalizer_to_storefwd", "TODO", "TODO", "TODO", "TODO", "-"))
	t.Log(evalCSVRow(
		"storefwd_to_bos_grpc", "TODO", "TODO", "TODO", "TODO", "-"))
}
