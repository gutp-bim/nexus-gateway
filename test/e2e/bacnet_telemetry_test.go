// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// TestBACnetTelemetryE2E (#40, EP-009 FEAT-038) — with the bacnet profile up
// (bbc-sim + bacnet-connector + nats, host networking for Who-Is/I-Am), the
// BACnet connector discovers and reads/COV-subscribes the sim's objects and
// publishes Common Events carrying protocol=bacnet and native object addressing
// only (ADR-0001).
//
// Acceptance:
//   - a Common Event for a known bbc-sim object arrives on evt.bacnet.bacnet-01
//   - it carries protocol=bacnet and a native "type,instance" local_id
//
// Note: BACnet discovery needs UDP broadcast; on a runner that cannot host it,
// run this against a directed (configured-IP) read instead (#40).
func TestBACnetTelemetryE2E(t *testing.T) {
	_, js := connectNATS(t)

	// Objects modelled by bbc-sim and declared in the shared Point List.
	want := map[string]bool{
		"analogInput,1001": true, // Supply Air Temperature (PT001)
		"analogInput,1003": true, // CO2 (PT008)
	}
	ev := awaitCommonEvent(t, js, "bacnet", "bacnet-01", want, 30*time.Second)

	if ev.Protocol != "bacnet" {
		t.Fatalf("protocol = %q, want bacnet", ev.Protocol)
	}
	if ev.LocalID == "" {
		t.Fatal("Common Event must carry the native object address as local_id")
	}
	t.Logf("BACnet telemetry observed: %s = %g %s", ev.LocalID, ev.Value, ev.Unit)
}
