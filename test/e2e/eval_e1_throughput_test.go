// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestE1_ThroughputScaling (research evaluation E1) measures nexus-gateway
// telemetry pipeline throughput as a function of point count and polling interval.
//
// Workload table (operator must configure the sim connector before running):
//
//	Point count  │  Interval   │  Expected event rate
//	─────────────┼─────────────┼─────────────────────
//	  100        │  1 s        │  100 events/s
//	  100        │  10 s       │   10 events/s
//	  100        │  60 s       │  1.7 events/s
//	 1000        │  1 s        │ 1000 events/s
//	 1000        │  10 s       │  100 events/s
//	 5000        │  1 s        │ 5000 events/s
//	10000        │  60 s       │  167 events/s
//
// Metrics collected per scenario (steady-state window, default 30 s):
//   - events/s         : Common Events published to JetStream EVENTS stream
//   - frames/s         : TelemetryFrames ≈ events/s after Normalizer (see note)
//   - nats_lag         : pending message count on EVENTS stream at window end
//   - sqlite_depth     : Store-and-Forward buffer depth from /metrics
//   - cpu_delta_s      : gateway process_cpu_seconds_total delta over window
//   - mem_mib          : gateway RSS in MiB from /metrics
//
// Note: frames/s cannot be measured directly without a mock Building OS that
// exposes a receive counter. For MVP the paper uses events/s as a proxy.
// When a mock ingress is wired (TODO: issue #43), read storefwd_sent_total
// from /metrics as the authoritative delivered-frames counter.
//
// Environment variables:
//
//	E2E_NATS_URL        — required; NATS URL (else test skips)
//	E2E_ADMIN_URL       — gateway Admin API (default http://localhost:8080)
//	E2E_E1_POINTS       — comma-sep point counts (default 100,1000,5000,10000)
//	E2E_E1_INTERVALS    — comma-sep intervals in seconds (default 1,10,60)
//	E2E_E1_WINDOW       — measurement window in seconds (default 30)
//
// Operator setup: set E2E_E1_POINTS=N and E2E_E1_INTERVALS=M on the sim
// connector container (e.g. via docker compose override), then run each
// scenario separately with the matching env vars here.
func TestE1_ThroughputScaling(t *testing.T) {
	requireEnv(t, "E2E_NATS_URL")
	adminURL := getenvOr("E2E_ADMIN_URL", "http://localhost:8080")
	pointCounts := parseIntListEnv(t, "E2E_E1_POINTS", []int{100, 1000, 5000, 10000})
	intervalsSec := parseIntListEnv(t, "E2E_E1_INTERVALS", []int{1, 10, 60})
	windowSec := parseIntEnv(t, "E2E_E1_WINDOW", 30)

	_, js := connectNATS(t)

	type row struct {
		pts, ivSec              int
		eventsPerSec, framesPS  float64
		natsLag, sqliteDepth    int64
		cpuDelta, memMiB        float64
	}
	var rows []row

	for _, pts := range pointCounts {
		for _, ivSec := range intervalsSec {
			name := fmt.Sprintf("pts=%d/iv=%ds", pts, ivSec)
			t.Run(name, func(t *testing.T) {
				// Warm-up: discard the first 5 s to let steady state settle.
				time.Sleep(5 * time.Second)

				mBefore := scrapeGatewayMetrics(t, adminURL)

				ctx, cancel := context.WithTimeout(context.Background(),
					time.Duration(windowSec+5)*time.Second)
				defer cancel()

				cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
					AckPolicy:     jetstream.AckNonePolicy,
					DeliverPolicy: jetstream.DeliverNewPolicy,
				})
				if err != nil {
					t.Skipf("EVENTS stream not available: %v", err)
				}

				start := time.Now()
				var eventCount int64
				deadline := time.NewTimer(time.Duration(windowSec) * time.Second)
				defer deadline.Stop()

			drain:
				for {
					select {
					case <-deadline.C:
						break drain
					default:
						msgs, fetchErr := cons.Fetch(512,
							jetstream.FetchMaxWait(500*time.Millisecond))
						if fetchErr != nil {
							continue
						}
						for range msgs.Messages() {
							eventCount++
						}
					}
				}
				elapsed := time.Since(start).Seconds()

				mAfter := scrapeGatewayMetrics(t, adminURL)
				cpuDelta := diffMetric(mBefore, mAfter, "process_cpu_seconds_total")
				memMiB := mAfter["process_resident_memory_bytes"] / (1024 * 1024)
				sqliteDepth := int64(mAfter["storefwd_buffer_depth"])

				// NATS lag: messages remaining in EVENTS stream (unconsumed by Normalizer).
				var natsLag int64
				if si, err := js.Stream(ctx, "EVENTS"); err == nil {
					natsLag = int64(si.CachedInfo().State.Msgs)
				}

				evPS := float64(eventCount) / elapsed
				r := row{
					pts: pts, ivSec: ivSec,
					eventsPerSec: evPS, framesPS: evPS,
					natsLag: natsLag, sqliteDepth: sqliteDepth,
					cpuDelta: cpuDelta, memMiB: memMiB,
				}
				rows = append(rows, r)

				t.Logf("E1 [%s]: events/s=%.1f lag=%d sqlite_depth=%d cpu_delta=%.3fs mem=%.0fMiB",
					name, evPS, natsLag, sqliteDepth, cpuDelta, memMiB)

				_ = adminURL // suppress unused warning when metrics endpoint absent
			})
		}
	}

	// Emit TSV for Table 1 in the paper.
	t.Log("E1 results (CSV — copy into paper Table 1):")
	t.Log("points,interval_s,events_per_s,frames_per_s,nats_lag,sqlite_depth,cpu_delta_s,mem_mib")
	for _, r := range rows {
		t.Log(evalCSVRow(
			r.pts, r.ivSec,
			fmt.Sprintf("%.2f", r.eventsPerSec),
			fmt.Sprintf("%.2f", r.framesPS),
			r.natsLag, r.sqliteDepth,
			fmt.Sprintf("%.3f", r.cpuDelta),
			fmt.Sprintf("%.1f", r.memMiB),
		))
	}
}
