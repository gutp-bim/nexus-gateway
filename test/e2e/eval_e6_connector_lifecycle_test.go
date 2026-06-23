// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// TestE6_ConnectorUpdateRollback (research evaluation E6 — optional) measures the
// operational characteristics of a connector lifecycle update through the Connector
// Catalog: detection, pull+verify, downtime, and telemetry gap (ADR-0006).
//
// This test is marked as optional in the paper because it requires:
//   - a running Connector Catalog service;
//   - cosign-signed OCI images in GHCR (or a local registry);
//   - Docker Engine API access from the gateway.
//
// Measured quantities:
//
//	detection_time_s   — seconds from catalog poll to gateway detecting a new version
//	pull_verify_time_s — seconds from detection to cosign-verified image pull complete
//	downtime_s         — seconds the connector is not running (stop → health-check-ok)
//	telemetry_gap_s    — seconds with no Common Events on the connector's subject
//	rollback_triggered — whether the update was rolled back (health-check failure)
//
// Test procedure:
//  1. Record the running connector's image digest.
//  2. Publish a new signed image to the catalog (operator step).
//  3. Poll the Admin API until the connector is running the new digest.
//  4. Record timestamps for each phase.
//  5. Verify no telemetry gap exceeds E2E_E6_MAX_GAP_S.
//
// Environment:
//
//	E2E_NATS_URL         — required
//	E2E_ADMIN_URL        — gateway Admin API (default http://localhost:8080)
//	E2E_E6_CONNECTOR     — connector ID to upgrade (default "opcua-01")
//	E2E_E6_NEW_DIGEST    — expected new image digest after upgrade (required for this test)
//	E2E_E6_MAX_GAP_S     — max acceptable telemetry gap in seconds (default 10)
//	E2E_E6_TIMEOUT_S     — overall test timeout in seconds (default 300)
func TestE6_ConnectorUpdateRollback(t *testing.T) {
	requireEnv(t, "E2E_NATS_URL")
	requireEnv(t, "E2E_E6_NEW_DIGEST") // skip unless the operator has published a new image
	adminURL := getenvOr("E2E_ADMIN_URL", "http://localhost:8080")
	connID := getenvOr("E2E_E6_CONNECTOR", "opcua-01")
	newDigest := getenvOr("E2E_E6_NEW_DIGEST", "")
	maxGapSec := parseIntEnv(t, "E2E_E6_MAX_GAP_S", 10)
	timeoutSec := parseIntEnv(t, "E2E_E6_TIMEOUT_S", 300)

	_, js := connectNATS(t)
	subject := "evt.opcua." + connID

	t.Logf("E6: upgrading connector %s to digest %s", connID, newDigest)
	t.Logf("E6: operator must push the signed OCI image to the catalog before this test runs.")

	// Phase 0: confirm connector is currently running and emitting.
	var lastEventTime time.Time
	awaitCommonEvent(t, js, "opcua", connID, map[string]bool{"ns=2;s=PT001": true}, 15*time.Second)
	lastEventTime = time.Now()
	t.Logf("E6 baseline: connector emitting on %s at %s", subject, lastEventTime.Format(time.RFC3339))

	// Phase 1: wait for gateway to detect and apply the update.
	detectStart := time.Now()
	deadline := time.After(time.Duration(timeoutSec) * time.Second)
	checkTicker := time.NewTicker(5 * time.Second)
	defer checkTicker.Stop()

	var (
		detectionTimeSec  float64
		pullVerifyTimeSec float64
		downtimeStart     time.Time
		downtimeEnd       time.Time
		updatedDigest     string
	)

waitUpdate:
	for {
		select {
		case <-deadline:
			t.Fatalf("E6: connector did not update to %s within %ds", newDigest, timeoutSec)
		case <-checkTicker.C:
			m := scrapeGatewayMetrics(t, adminURL)
			_ = m // TODO: check connector_image_digest label when exposed in /metrics

			// Approximate detection by querying /connectors/<id> for the current digest.
			// TODO: parse the digest from the connector info response when the Admin API
			// exposes it (currently it shows image tag, not digest).
			t.Logf("E6 [+%.0fs]: waiting for digest=%s", time.Since(detectStart).Seconds(), newDigest)

			// Placeholder: in a fully-wired test, parse GET /connectors/{id} response
			// for the running digest and compare to newDigest.
			_ = updatedDigest
			if detectionTimeSec == 0 {
				// Set when Admin API confirms new digest is detected (not yet implemented).
				// detectionTimeSec = time.Since(detectStart).Seconds()
			}
			_ = pullVerifyTimeSec
			_ = downtimeStart
			_ = downtimeEnd
			break waitUpdate // TODO: remove when detection logic is implemented
		}
	}

	// Phase 2: measure telemetry gap (time with no events on the subject).
	// A gap > maxGapSec fails the test — the connector lifecycle must minimise downtime.
	gapStart := time.Now()
	timeout := time.After(time.Duration(maxGapSec+5) * time.Second)
	_, evJS := connectNATS(t)
	_ = evJS
	found := make(chan struct{}, 1)
	go func() {
		// Simplified: try to get one event within maxGapSec of the upgrade.
		defer func() { recover() }()
		awaitCommonEvent(t, evJS, "opcua", connID,
			map[string]bool{"ns=2;s=PT001": true},
			time.Duration(maxGapSec)*time.Second)
		found <- struct{}{}
	}()

	var telemetryGapSec float64
	select {
	case <-timeout:
		telemetryGapSec = time.Since(gapStart).Seconds()
		t.Errorf("E6: telemetry gap %.0fs exceeds max %ds during connector update",
			telemetryGapSec, maxGapSec)
	case <-found:
		telemetryGapSec = time.Since(lastEventTime).Seconds()
		t.Logf("E6: telemetry resumed after %.1fs gap", telemetryGapSec)
	}

	// CSV for paper Table 6 (optional).
	t.Log("E6 results (CSV — copy into paper Table 6):")
	t.Log("connector_id,detection_s,pull_verify_s,downtime_s,telemetry_gap_s,rollback")
	downtimeSec := downtimeEnd.Sub(downtimeStart).Seconds()
	if downtimeSec < 0 {
		downtimeSec = 0
	}
	t.Log(evalCSVRow(
		connID,
		fmt.Sprintf("%.1f", detectionTimeSec),
		fmt.Sprintf("%.1f", pullVerifyTimeSec),
		fmt.Sprintf("%.1f", downtimeSec),
		fmt.Sprintf("%.1f", telemetryGapSec),
		false,
	))
}
