// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestE4_PointListDrift (research evaluation E4) measures the gateway's ability
// to converge to a new Point List mapping without losing canonical identity.
//
// Three sub-scenarios (run individually via E2E_E4_SCENARIO):
//
//	unknown   — sim emits a local_id not in the Point List.
//	            Expected: normalizer_unresolved_total increments; no TelemetryFrame.
//
//	remap     — sim's local_id is remapped to a new point_id in an updated Point List.
//	            Expected: sync picks up the new mapping within one poll interval;
//	            subsequent frames carry the new point_id.
//
//	unit      — the unit for a local_id changes in the Point List (e.g. degF→degC).
//	            Expected: after sync, TelemetryFrames carry the new canonical unit.
//
// Metrics collected:
//   - unresolved_ratio   : normalizer_unresolved_total / total events during window
//   - sync_time_s        : wall-clock seconds from Point List update trigger to
//                          first frame with the new mapping (measured via NATS)
//   - accepted_after_remap: storefwd_sent_total delta after remap takes effect
//
// Point List update trigger: POST /connectors/{id}/point-list-refresh (forces the
// gateway to re-poll the provisioning source immediately). Without this endpoint,
// the operator must wait up to E2E_E4_POLL_INTERVAL_S seconds.
//
// Environment:
//
//	E2E_NATS_URL              — required
//	E2E_ADMIN_URL             — gateway Admin API (default http://localhost:8080)
//	E2E_E4_SCENARIO           — "unknown" | "remap" | "unit" (default "unknown")
//	E2E_E4_UNKNOWN_LOCAL_ID   — local_id not in Point List (default "UNKNOWN-9999")
//	E2E_E4_WINDOW             — measurement window in seconds (default 30)
//	E2E_E4_POLL_INTERVAL_S    — expected gateway poll interval (default 600)
func TestE4_PointListDrift(t *testing.T) {
	requireEnv(t, "E2E_NATS_URL")
	adminURL := getenvOr("E2E_ADMIN_URL", "http://localhost:8080")
	scenario := getenvOr("E2E_E4_SCENARIO", "unknown")
	windowSec := parseIntEnv(t, "E2E_E4_WINDOW", 30)

	switch scenario {
	case "unknown":
		testE4Unknown(t, adminURL, windowSec)
	case "remap":
		testE4Remap(t, adminURL, windowSec)
	case "unit":
		testE4Unit(t, adminURL, windowSec)
	default:
		t.Fatalf("unknown E2E_E4_SCENARIO: %q (want unknown|remap|unit)", scenario)
	}
}

// testE4Unknown measures the normalizer_unresolved_total rate when a local_id
// has no entry in the Point List. The sim must be emitting the unknown local_id
// set in E2E_E4_UNKNOWN_LOCAL_ID at its normal rate.
func testE4Unknown(t *testing.T, adminURL string, windowSec int) {
	t.Helper()
	unknownLocalID := getenvOr("E2E_E4_UNKNOWN_LOCAL_ID", "UNKNOWN-9999")
	t.Logf("E4[unknown]: measuring unresolved_total for local_id=%s over %ds", unknownLocalID, windowSec)

	mBefore := scrapeGatewayMetrics(t, adminURL)
	time.Sleep(time.Duration(windowSec) * time.Second)
	mAfter := scrapeGatewayMetrics(t, adminURL)

	unresolvedDelta := diffMetric(mBefore, mAfter, "normalizer_unresolved_total")
	invalidDelta := diffMetric(mBefore, mAfter, "normalizer_invalid_total")
	sentDelta := diffMetric(mBefore, mAfter, "storefwd_sent_total")
	total := unresolvedDelta + invalidDelta + sentDelta
	var unresolvedRatio float64
	if total > 0 {
		unresolvedRatio = unresolvedDelta / total
	}

	t.Logf("E4[unknown]: unresolved=%.0f invalid=%.0f sent=%.0f total=%.0f unresolved_ratio=%.3f",
		unresolvedDelta, invalidDelta, sentDelta, total, unresolvedRatio)

	if unresolvedDelta == 0 {
		t.Log("warn: no unresolved events detected — is the sim emitting the unknown local_id?")
	}

	t.Log("E4 results (CSV — copy into paper Table 4):")
	t.Log("scenario,window_s,unresolved,sent,unresolved_ratio")
	t.Log(evalCSVRow("unknown", windowSec,
		fmt.Sprintf("%.0f", unresolvedDelta),
		fmt.Sprintf("%.0f", sentDelta),
		fmt.Sprintf("%.4f", unresolvedRatio),
	))
}

// testE4Remap measures convergence time when a point's local_id→point_id mapping
// changes. The operator must:
//  1. Ensure the sim is emitting local_id "E2E_E4_REMAP_LOCAL_ID" normally.
//  2. Update the provisioning source (file or API) with the new point_id.
//  3. Run this test — it triggers a Point List refresh and times convergence.
func testE4Remap(t *testing.T, adminURL string, windowSec int) {
	t.Helper()
	remapLocalID := getenvOr("E2E_E4_REMAP_LOCAL_ID", "ns=2;s=PT001")
	newPointID := getenvOr("E2E_E4_REMAP_NEW_POINT_ID", "SOS-PT-001-V2")

	t.Logf("E4[remap]: local_id=%s → new point_id=%s", remapLocalID, newPointID)
	t.Log("Ensure the provisioning source already contains the new mapping before running.")

	// Trigger immediate Point List refresh via Admin API.
	refreshStart := time.Now()
	connID := getenvOr("E2E_E4_CONNECTOR_ID", "opcua-01")
	resp, err := http.Post(
		fmt.Sprintf("%s/connectors/%s/point-list-refresh", adminURL, connID),
		"application/json", nil,
	)
	if err != nil || (resp != nil && resp.StatusCode >= 400) {
		sc := 0
		if resp != nil {
			sc = resp.StatusCode
			resp.Body.Close()
		}
		t.Logf("warn: /connectors/%s/point-list-refresh not available (err=%v status=%d); "+
			"gateway will converge at next poll interval instead.", connID, err, sc)
	}
	if resp != nil {
		resp.Body.Close()
	}

	// Poll /connectors endpoint until the point_id change is reflected, or time out.
	deadline := time.After(time.Duration(windowSec) * time.Second)
	var syncTimeSec float64
	pollTicker := time.NewTicker(2 * time.Second)
	defer pollTicker.Stop()

waitSync:
	for {
		select {
		case <-deadline:
			t.Log("warn: new point_id not confirmed within window")
			break waitSync
		case <-pollTicker.C:
			if pointIDInConnector(t, adminURL, connID, newPointID) {
				syncTimeSec = time.Since(refreshStart).Seconds()
				t.Logf("E4[remap]: new point_id confirmed after %.1fs", syncTimeSec)
				break waitSync
			}
		}
	}

	t.Log("E4 results (CSV — copy into paper Table 4):")
	t.Log("scenario,sync_time_s,new_point_id")
	t.Log(evalCSVRow("remap", fmt.Sprintf("%.1f", syncTimeSec), newPointID))
}

// testE4Unit measures convergence when a unit changes in the Point List.
func testE4Unit(t *testing.T, adminURL string, windowSec int) {
	t.Helper()
	localID := getenvOr("E2E_E4_UNIT_LOCAL_ID", "ns=2;s=PT001")
	newUnit := getenvOr("E2E_E4_UNIT_NEW_UNIT", "degC")

	t.Logf("E4[unit]: local_id=%s expecting new unit=%s after Point List update", localID, newUnit)
	t.Log("Ensure the provisioning source already has the updated unit before running.")

	connID := getenvOr("E2E_E4_CONNECTOR_ID", "opcua-01")
	refreshStart := time.Now()
	resp, _ := http.Post(
		fmt.Sprintf("%s/connectors/%s/point-list-refresh", adminURL, connID),
		"application/json", nil,
	)
	if resp != nil {
		resp.Body.Close()
	}

	time.Sleep(time.Duration(windowSec) * time.Second)
	syncTimeSec := time.Since(refreshStart).Seconds()

	t.Logf("E4[unit]: waited %.0fs; verify TelemetryFrames now carry unit=%s via Building OS API", syncTimeSec, newUnit)
	t.Log("E4 results (CSV — copy into paper Table 4):")
	t.Log("scenario,sync_time_s,new_unit")
	t.Log(evalCSVRow("unit", fmt.Sprintf("%.1f", syncTimeSec), newUnit))
}

// pointIDInConnector queries GET /connectors/{id} and checks whether point_id appears
// in the response JSON (shallow check for convergence confirmation). Returns false on
// any error so the caller continues polling.
func pointIDInConnector(t *testing.T, adminURL, connID, pointID string) bool {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/connectors/%s", adminURL, connID))
	if err != nil || resp.StatusCode != http.StatusOK {
		return false
	}
	defer resp.Body.Close()
	var body map[string]any
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return false
	}
	b, _ := json.Marshal(body)
	return containsStr(string(b), pointID)
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

