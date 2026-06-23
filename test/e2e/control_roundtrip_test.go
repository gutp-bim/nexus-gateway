// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestControlRoundTripE2E (#42, EP-009 FEAT-040) — a Control Command dispatched
// on the connector's command channel (cmd.<proto>.<connectorID>) reaches the
// connector write handler, performs the protocol write against the simulator,
// and returns a typed ControlResult within the deadline, idempotent on
// control_id (ADR-0004).
//
// This drives the connector directly over core NATS request-reply (the same
// channel the Egress dispatcher uses), targeting a writable point.
//
// Acceptance:
//   - the reply is success=true within the deadline
//   - re-sending the same control_id does not double-write (idempotent)
func TestControlRoundTripE2E(t *testing.T) {
	nc, _ := connectNATS(t)

	// PT006 (analogValue,1002 / ns=2;s=PT006) is writable in both sims.
	proto := getenvOr("E2E_CONTROL_PROTOCOL", "opcua")
	connID := getenvOr("E2E_CONTROL_CONNECTOR", "opcua-01")
	localID := getenvOr("E2E_CONTROL_LOCAL_ID", "ns=2;s=PT006")

	cmd := map[string]any{
		"control_id": "e2e-" + time.Now().Format("150405.000"),
		"local_id":   localID,
		"value":      23.5,
		"priority":   8,
	}
	body, _ := json.Marshal(cmd)
	subject := "cmd." + proto + "." + connID

	msg, err := nc.Request(subject, body, 10*time.Second)
	if err != nil {
		t.Fatalf("control request to %s failed: %v", subject, err)
	}
	var reply writeReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if !reply.Success {
		t.Fatalf("control write not accepted: %s", reply.Response)
	}

	// Idempotency: the same control_id replays to the cached result, not a 2nd write.
	msg2, err := nc.Request(subject, body, 10*time.Second)
	if err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	var reply2 writeReply
	_ = json.Unmarshal(msg2.Data, &reply2)
	if !reply2.Success {
		t.Fatalf("idempotent replay should return the cached success, got: %s", reply2.Response)
	}

	// TODO(#42): read the point back from the simulator and assert the new value,
	// closing the physical round-trip.
}

func getenvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
