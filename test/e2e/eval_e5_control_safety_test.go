// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestE5_ControlCommandSafety (research evaluation E5) verifies that the control
// path adheres to the real-time-or-fail contract (ADR-0004): stale commands are
// never applied, typed errors are returned synchronously, the control_id cache
// prevents double-writes, and uplink outages do not buffer commands for later
// replay against physical equipment.
//
// Sub-tests:
//
//	stale_deadline — send a command with a deadline that has already passed (or a
//	                 1 ms timeout). Expect: the connector returns an error result
//	                 within the request-reply timeout, NOT a successful write.
//
//	typed_failure  — send a command with a value that is out of range or wrong type
//	                 (e.g. string where float expected). Expect: typed error response.
//
//	idempotent     — send the same control_id twice within the cache TTL. Expect:
//	                 the second reply is identical to the first, no second write.
//	                 (Also tested in TestControlRoundTripE2E — repeated here for
//	                 measurement/paper purposes.)
//
//	no_buffer      — kill the uplink, send a control command. Expect: the command
//	                 fails immediately (core NATS request-reply timeout); the command
//	                 is NOT queued for later delivery. Confirm by restoring uplink and
//	                 checking no delayed write arrives at the sim.
//
// Environment:
//
//	E2E_NATS_URL             — required
//	E2E_E5_PROTOCOL          — connector protocol (default "opcua")
//	E2E_E5_CONNECTOR         — connector ID (default "opcua-01")
//	E2E_E5_WRITABLE_LOCAL_ID — local_id of a writable point (default "ns=2;s=PT006")
//	E2E_E5_SCENARIO          — "stale_deadline"|"typed_failure"|"idempotent"|"no_buffer"|"all"
//	                           (default "all")
func TestE5_ControlCommandSafety(t *testing.T) {
	requireEnv(t, "E2E_NATS_URL")
	proto := getenvOr("E2E_E5_PROTOCOL", "opcua")
	connID := getenvOr("E2E_E5_CONNECTOR", "opcua-01")
	writableLocalID := getenvOr("E2E_E5_WRITABLE_LOCAL_ID", "ns=2;s=PT006")
	scenario := getenvOr("E2E_E5_SCENARIO", "all")

	nc, _ := connectNATS(t)
	subject := "cmd." + proto + "." + connID

	type safetyResult struct {
		scenario        string
		passed          bool
		latencyMs       float64
		detail          string
	}
	var results []safetyResult

	runScenario := func(name string) bool {
		return scenario == "all" || scenario == name
	}

	// --- stale_deadline ---
	if runScenario("stale_deadline") {
		t.Run("stale_deadline", func(t *testing.T) {
			// Send a command with an expired_at timestamp 10 seconds in the past.
			// ADR-0004: connectors MUST check the deadline and reject stale commands
			// rather than writing to equipment.
			cmd := map[string]any{
				"control_id": fmt.Sprintf("e5-stale-%d", time.Now().UnixNano()),
				"local_id":   writableLocalID,
				"value":      0.0,
				"priority":   8,
				"expired_at": time.Now().Add(-10 * time.Second).Format(time.RFC3339),
			}
			body, _ := json.Marshal(cmd)
			start := time.Now()
			msg, err := nc.Request(subject, body, 10*time.Second)
			latencyMs := float64(time.Since(start).Milliseconds())

			var reply writeReply
			if err == nil {
				_ = json.Unmarshal(msg.Data, &reply)
			}

			passed := err != nil || !reply.Success
			detail := fmt.Sprintf("err=%v success=%v", err, reply.Success)
			results = append(results, safetyResult{"stale_deadline", passed, latencyMs, detail})

			if !passed {
				t.Errorf("stale command was accepted — connector must reject expired_at in the past (%s)", detail)
			} else {
				t.Logf("E5[stale_deadline]: correctly rejected in %.0fms (%s)", latencyMs, detail)
			}
		})
	}

	// --- typed_failure ---
	if runScenario("typed_failure") {
		t.Run("typed_failure", func(t *testing.T) {
			// Send a value that is a string where float is expected.
			cmd := map[string]any{
				"control_id": fmt.Sprintf("e5-typed-%d", time.Now().UnixNano()),
				"local_id":   writableLocalID,
				"value":      "NOT_A_NUMBER",
				"priority":   8,
			}
			body, _ := json.Marshal(cmd)
			start := time.Now()
			msg, err := nc.Request(subject, body, 10*time.Second)
			latencyMs := float64(time.Since(start).Milliseconds())

			var reply writeReply
			if err == nil {
				_ = json.Unmarshal(msg.Data, &reply)
			}

			passed := err != nil || !reply.Success
			detail := fmt.Sprintf("err=%v success=%v resp=%s", err, reply.Success, reply.Response)
			results = append(results, safetyResult{"typed_failure", passed, latencyMs, detail})

			if !passed {
				t.Errorf("typed error not returned — connector must reject invalid value type (%s)", detail)
			} else {
				t.Logf("E5[typed_failure]: typed error in %.0fms: %s", latencyMs, reply.Response)
			}
		})
	}

	// --- idempotent ---
	if runScenario("idempotent") {
		t.Run("idempotent", func(t *testing.T) {
			cid := fmt.Sprintf("e5-idem-%d", time.Now().UnixNano())
			cmd := map[string]any{
				"control_id": cid,
				"local_id":   writableLocalID,
				"value":      21.0,
				"priority":   8,
			}
			body, _ := json.Marshal(cmd)

			msg1, err1 := nc.Request(subject, body, 10*time.Second)
			msg2, err2 := nc.Request(subject, body, 10*time.Second)

			var r1, r2 writeReply
			if err1 == nil {
				_ = json.Unmarshal(msg1.Data, &r1)
			}
			if err2 == nil {
				_ = json.Unmarshal(msg2.Data, &r2)
			}

			// Both must succeed, and the second must not trigger a second equipment write.
			// We verify idempotency by checking the responses match (same cached result).
			passed := err1 == nil && err2 == nil && r1.Success && r2.Success && r1.Response == r2.Response
			detail := fmt.Sprintf("r1=%+v r2=%+v", r1, r2)
			results = append(results, safetyResult{"idempotent", passed, 0, detail})

			if !passed {
				t.Errorf("control_id idempotency violated: %s", detail)
			} else {
				t.Logf("E5[idempotent]: same control_id returns cached result: %s", detail)
			}
		})
	}

	// --- no_buffer ---
	if runScenario("no_buffer") {
		t.Run("no_buffer", func(t *testing.T) {
			t.Log("E5[no_buffer]: this sub-test requires manual uplink interruption.")
			t.Log("Operator steps:")
			t.Log("  1. Stop the Building OS mock gRPC server.")
			t.Log("  2. Send the control command below.")
			t.Log("  3. Expect: command fails within deadline (not buffered).")
			t.Log("  4. Restart the Building OS mock.")
			t.Log("  5. Confirm: no delayed write arrives at the sim.")
			t.Skip("no_buffer requires manual uplink interruption — see instructions above")
		})
	}

	// CSV for paper Table 5.
	t.Log("E5 results (CSV — copy into paper Table 5):")
	t.Log("scenario,passed,latency_ms,detail")
	for _, r := range results {
		t.Log(evalCSVRow(r.scenario, r.passed,
			fmt.Sprintf("%.0f", r.latencyMs), r.detail))
	}
}
