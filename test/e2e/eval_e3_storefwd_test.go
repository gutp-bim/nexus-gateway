// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestE3_StoreFwdRecovery (research evaluation E3) measures the Store-and-Forward
// SQLite ring buffer's behaviour during a Building OS uplink outage, then the
// recovery drain rate and completeness after the uplink is restored.
//
// Test procedure:
//  1. Confirm telemetry is flowing (storefwd_sent_total increasing).
//  2. Trigger an uplink outage by hitting the gateway's Admin API kill endpoint
//     (POST /debug/kill-uplink), or by manually stopping the Building OS mock.
//  3. Wait E2E_E3_OUTAGE_SEC seconds.
//  4. During the outage record: storefwd_buffer_depth, storefwd_dropped_total.
//  5. Restore the uplink (POST /debug/restore-uplink or restart the mock).
//  6. Wait for storefwd_buffer_depth to return to zero.
//  7. Record: buffered_count, dropped_count, recovery_time_s, sent_after_recovery.
//
// Outage durations from the paper's experimental plan:
//
//	E2E_E3_OUTAGE_SEC = 60   (1 minute)
//	E2E_E3_OUTAGE_SEC = 600  (10 minutes)
//	E2E_E3_OUTAGE_SEC = 1800 (30 minutes)
//	E2E_E3_OUTAGE_SEC = 3600 (60 minutes — near ring-buffer capacity limit)
//
// With 100 points @ 1 s interval and a 1 M-row SQLite buffer:
//   - 1 min outage  →   6 000 events → expect zero drops
//   - 10 min outage →  60 000 events → expect zero drops
//   - 60 min outage → 360 000 events → may approach capacity; expect some drops
//
// Note: the /debug/kill-uplink endpoint does not yet exist in the Admin API.
// Until issue #XX adds it, the operator must manually stop the Building OS mock
// between the "pre-outage" and "post-outage" metric snapshots.
//
// Environment:
//
//	E2E_NATS_URL         — required
//	E2E_ADMIN_URL        — gateway Admin API (default http://localhost:8080)
//	E2E_E3_OUTAGE_SEC    — outage duration in seconds (default 60)
//	E2E_E3_RECOVERY_WAIT — max seconds to wait for buffer drain (default 300)
func TestE3_StoreFwdRecovery(t *testing.T) {
	requireEnv(t, "E2E_NATS_URL")
	adminURL := getenvOr("E2E_ADMIN_URL", "http://localhost:8080")
	outageSec := parseIntEnv(t, "E2E_E3_OUTAGE_SEC", 60)
	recoveryWaitSec := parseIntEnv(t, "E2E_E3_RECOVERY_WAIT", 300)

	// Phase 1: baseline — confirm telemetry is flowing.
	mBase := scrapeGatewayMetrics(t, adminURL)
	baselineSent := mBase["storefwd_sent_total"]
	t.Logf("E3 baseline: storefwd_sent_total=%.0f buffer_depth=%.0f",
		baselineSent, mBase["storefwd_buffer_depth"])

	// Give a few seconds to confirm flow.
	time.Sleep(5 * time.Second)
	mCheck := scrapeGatewayMetrics(t, adminURL)
	if mCheck["storefwd_sent_total"] <= baselineSent {
		t.Log("warn: storefwd_sent_total is not increasing — is telemetry flowing?")
	}

	// Phase 2: trigger outage.
	// Try the debug endpoint; if absent (404/405) the operator must act manually.
	t.Logf("E3 triggering uplink outage (%d s)…", outageSec)
	killResp, err := http.Post(adminURL+"/debug/kill-uplink", "application/json", nil)
	if err != nil || (killResp != nil && killResp.StatusCode >= 400) {
		sc := 0
		if killResp != nil {
			sc = killResp.StatusCode
			killResp.Body.Close()
		}
		t.Logf("warn: /debug/kill-uplink not available (err=%v status=%d); "+
			"manually stop Building OS mock now, then wait %d s, then press Ctrl-C "+
			"to skip or set E2E_E3_OUTAGE_SEC=0 to skip.", err, sc, outageSec)
	}
	if killResp != nil {
		killResp.Body.Close()
	}

	// Phase 3: wait out the outage, sample buffer depth periodically.
	outageStart := time.Now()
	var maxDepth int64
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	outageTimer := time.NewTimer(time.Duration(outageSec) * time.Second)
	defer outageTimer.Stop()

outageDrain:
	for {
		select {
		case <-outageTimer.C:
			break outageDrain
		case <-ticker.C:
			m := scrapeGatewayMetrics(t, adminURL)
			depth := int64(m["storefwd_buffer_depth"])
			if depth > maxDepth {
				maxDepth = depth
			}
			t.Logf("E3 [outage +%.0fs]: buffer_depth=%d dropped=%.0f",
				time.Since(outageStart).Seconds(), depth, m["storefwd_dropped_total"])
		}
	}
	mAtOutageEnd := scrapeGatewayMetrics(t, adminURL)
	bufferedAtEnd := int64(mAtOutageEnd["storefwd_buffer_depth"])
	droppedTotal := mAtOutageEnd["storefwd_dropped_total"]

	// Phase 4: restore uplink.
	t.Log("E3 restoring uplink…")
	restoreResp, _ := http.Post(adminURL+"/debug/restore-uplink", "application/json", nil)
	if restoreResp != nil {
		restoreResp.Body.Close()
	}
	recoveryStart := time.Now()

	// Phase 5: wait for buffer drain.
	recoveryTimer := time.NewTimer(time.Duration(recoveryWaitSec) * time.Second)
	defer recoveryTimer.Stop()
	checkTicker := time.NewTicker(5 * time.Second)
	defer checkTicker.Stop()

	var recoveryTimeSec float64

waitRecovery:
	for {
		select {
		case <-recoveryTimer.C:
			t.Logf("warn: buffer did not drain within %d s", recoveryWaitSec)
			break waitRecovery
		case <-checkTicker.C:
			m := scrapeGatewayMetrics(t, adminURL)
			depth := int64(m["storefwd_buffer_depth"])
			t.Logf("E3 [recovery +%.0fs]: buffer_depth=%d",
				time.Since(recoveryStart).Seconds(), depth)
			if depth == 0 {
				recoveryTimeSec = time.Since(recoveryStart).Seconds()
				break waitRecovery
			}
		}
	}

	mFinal := scrapeGatewayMetrics(t, adminURL)
	sentAfterRecovery := diffMetric(mAtOutageEnd, mFinal, "storefwd_sent_total")

	t.Logf("E3 summary: outage=%ds max_depth=%d buffered_at_end=%d dropped=%.0f recovery_s=%.1f sent_after=%.0f",
		outageSec, maxDepth, bufferedAtEnd, droppedTotal, recoveryTimeSec, sentAfterRecovery)

	// CSV for paper Table 3.
	t.Log("E3 results (CSV — copy into paper Table 3):")
	t.Log("outage_s,max_buffer_depth,buffered_at_outage_end,dropped_total,recovery_s,sent_after_recovery")
	t.Log(evalCSVRow(
		outageSec, maxDepth, bufferedAtEnd,
		fmt.Sprintf("%.0f", droppedTotal),
		fmt.Sprintf("%.1f", recoveryTimeSec),
		fmt.Sprintf("%.0f", sentAfterRecovery),
	))
}
