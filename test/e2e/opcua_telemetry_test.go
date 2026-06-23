// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// TestOPCUATelemetryE2E (#39, EP-009 FEAT-039) — with the opcua profile up
// (opcua-sim + opcua-connector + nats), the OPC-UA connector browses/reads/
// subscribes the sim's address space and publishes Common Events carrying
// protocol=opcua and native NodeId addressing only (ADR-0001).
//
// Acceptance:
//   - a Common Event for a known opcua-sim NodeId arrives on evt.opcua.opcua-01
//   - it carries protocol=opcua, native local_id, and a numeric value
//   - (extended) the resulting TelemetryFrame reaches the mock Building OS ingress
func TestOPCUATelemetryE2E(t *testing.T) {
	_, js := connectNATS(t)

	// NodeIds modelled by opcua-sim and declared in the shared Point List.
	want := map[string]bool{
		"ns=2;s=PT001": true, // SupplyAirTemperature
		"ns=2;s=PT008": true, // CO2Level
	}
	ev := awaitCommonEvent(t, js, "opcua", "opcua-01", want, 30*time.Second)

	if ev.Protocol != "opcua" {
		t.Fatalf("protocol = %q, want opcua", ev.Protocol)
	}
	if ev.LocalID == "" {
		t.Fatal("Common Event must carry the native NodeId as local_id")
	}
	t.Logf("OPC-UA telemetry observed: %s = %g %s", ev.LocalID, ev.Value, ev.Unit)

	// TODO(#39): also assert the frame lands at the mock Building OS ingress
	// (decode TelemetryFrame, check (gateway_id, point_id) for this NodeId).
}
