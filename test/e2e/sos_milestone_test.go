// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestSoSIngestAndAPIE2E (#43/#44, EP-010 FEAT-043/044) — the full
// System-of-Systems path against a REAL Building OS stack, mirroring the
// building-os-e2e-test M4/M5 milestones:
//
//   simulators → nexus pipeline → gRPC GatewayIngress → building-os.validated.telemetry
//             → telemetry-consumer → TimescaleDB → GET /telemetries/hot?pointId=
//
// nexus delivers via the canonical gRPC GatewayIngress/StreamTelemetry interface
// (ADR-0001); identity is (gateway_id, point_id). This asserts each ingested
// point is retrievable by the canonical point_id the Normalizer assigned.
//
// Requires: a running Building OS OSS stack (ConnectorWorker gRPC ingress +
// telemetry-consumer + TimescaleDB + API) and the gateway pointed at it.
func TestSoSIngestAndAPIE2E(t *testing.T) {
	apiURL := requireEnv(t, "E2E_BOS_API_URL") // e.g. http://localhost:5000

	// Canonical point_ids from the shared SoS point list (mvp-pointlist.csv).
	pointIDs := []string{"SOS-PT-001", "SOS-PT-002"}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, pid := range pointIDs {
		if err := waitForHotTelemetry(ctx, apiURL, pid); err != nil {
			t.Fatalf("M4/M5: point %s never became queryable via the Building OS API: %v", pid, err)
		}
		t.Logf("SoS ingest+API ok: %s retrievable via /telemetries/hot", pid)
	}

	// TODO(#44): control round-trip (M7/M8) — issue a Building OS Egress control
	// request for a writable point, assert point_id↔local_id round-trips through
	// nexus to the simulator and the new reading flows back; non-writable points
	// are rejected at the gate. Uses the Building OS stack's own Keycloak.
	//
	// TODO(#45): emit these milestone results into the building-os-e2e-test
	// report format (reports/<test_run_id>/) for the HTML pipeline visualization.
}

// waitForHotTelemetry polls GET /telemetries/hot?pointId=<pid> until it returns
// a value or the context expires.
func waitForHotTelemetry(ctx context.Context, apiURL, pointID string) error {
	url := fmt.Sprintf("%s/telemetries/hot?pointId=%s", apiURL, pointID)
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			var body any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if body != nil {
				return nil
			}
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
	return ctx.Err()
}
