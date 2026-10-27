// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
package runcontext_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	rc "nexus-gateway/internal/runcontext"
)

func TestCommittedContextRequiresApplyAndSurvivesRecovery(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	scope := rc.Scope{GatewayID: "GW-EXAMPLE", ClockID: "urn:clock:test"}
	path := filepath.Join(t.TempDir(), "ledger.json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ledger, err := rc.Initialize(path, scope, public)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(rc.Context{SchemaVersion: "clock-run-context/2-draft", GatewayID: scope.GatewayID, ClockID: scope.ClockID, ProvisioningRevision: "rev-a", RunID: "run-a", Generation: 1, Status: "active", IssuedAt: now, ValidFrom: now, ExpiresAt: now.Add(time.Hour)})
	envelope := rc.Envelope{Payload: payload, Signature: ed25519.Sign(private, payload)}
	if err := ledger.Commit(envelope, now); err != nil {
		t.Fatal(err)
	}
	if got := ledger.Evaluate("rev-a", now.Add(time.Second), now.Add(2*time.Second)); got.Status != "unverified" {
		t.Fatalf("commit alone: %+v", got)
	}
	if err := ledger.Apply(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ledger, err = rc.Open(path, scope, public)
	if err != nil {
		t.Fatal(err)
	}
	got := ledger.Evaluate("rev-a", now.Add(2*time.Second), now.Add(3*time.Second))
	if got.Status != "valid" || got.RunID != "run-a" || got.Generation != 1 {
		t.Fatalf("recovered: %+v", got)
	}
}

func signed(t *testing.T, private ed25519.PrivateKey, context rc.Context) rc.Envelope {
	t.Helper()
	payload, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	return rc.Envelope{Payload: payload, Signature: ed25519.Sign(private, payload)}
}

func TestResetCannotBeClearedByRestartOrRenewingTheSameRun(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	scope := rc.Scope{GatewayID: "GW-EXAMPLE", ClockID: "urn:clock:test"}
	path := filepath.Join(t.TempDir(), "ledger.json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	context := rc.Context{SchemaVersion: "clock-run-context/2-draft", GatewayID: scope.GatewayID, ClockID: scope.ClockID, ProvisioningRevision: "rev-a", RunID: "run-a", Generation: 1, Status: "active", IssuedAt: now, ValidFrom: now, ExpiresAt: now.Add(time.Hour)}
	ledger, err := rc.Initialize(path, scope, public)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Commit(signed(t, private, context), now); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Apply(now); err != nil {
		t.Fatal(err)
	}
	if err = ledger.InvalidateRun("run-a"); err != nil {
		t.Fatal(err)
	}
	ledger, err = rc.Open(path, scope, public)
	if err != nil {
		t.Fatal(err)
	}
	context.Generation = 2
	if err = ledger.Commit(signed(t, private, context), now); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Apply(now); err != nil {
		t.Fatal(err)
	}
	if got := ledger.Evaluate("rev-a", now, now); got.Status != "invalid" {
		t.Fatalf("reset revived old run: %+v", got)
	}
	context.Generation = 3
	context.RunID = "run-b"
	if err = ledger.Commit(signed(t, private, context), now); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Apply(now); err != nil {
		t.Fatal(err)
	}
	if got := ledger.Evaluate("rev-a", now, now); got.Status != "valid" {
		t.Fatalf("new run: %+v", got)
	}
}

func TestPersistenceFailurePoisonsTheLiveLedger(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	scope := rc.Scope{GatewayID: "GW-EXAMPLE", ClockID: "urn:clock:test"}
	directory := t.TempDir()
	path := filepath.Join(directory, "ledger.json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	context := rc.Context{SchemaVersion: "clock-run-context/2-draft", GatewayID: scope.GatewayID, ClockID: scope.ClockID, ProvisioningRevision: "rev-a", RunID: "run-a", Generation: 1, Status: "active", IssuedAt: now, ValidFrom: now, ExpiresAt: now.Add(time.Hour)}
	ledger, err := rc.Initialize(path, scope, public)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Commit(signed(t, private, context), now); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Apply(now); err != nil {
		t.Fatal(err)
	}
	// Remove only this test's owned directory to simulate lost durable storage.
	if err = os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err = ledger.InvalidateRun("run-a"); err == nil {
		t.Fatal("expected storage failure")
	}
	if got := ledger.Evaluate("rev-a", now, now); got.Status != "unverified" {
		t.Fatalf("storage failure kept lease valid: %+v", got)
	}
}

func TestRejectsInvalidSignedContextsAndGenerationConflicts(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	scope := rc.Scope{GatewayID: "GW-EXAMPLE", ClockID: "urn:clock:test"}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base := rc.Context{SchemaVersion: "clock-run-context/2-draft", GatewayID: scope.GatewayID, ClockID: scope.ClockID, ProvisioningRevision: "rev-a", RunID: "run-a", Generation: 1, Status: "active", IssuedAt: now, ValidFrom: now, ExpiresAt: now.Add(time.Hour)}
	cases := map[string]func(*rc.Context){
		"gateway": func(c *rc.Context) { c.GatewayID = "other" }, "clock": func(c *rc.Context) { c.ClockID = "other" },
		"schema": func(c *rc.Context) { c.SchemaVersion = "unknown" }, "zero-generation": func(c *rc.Context) { c.Generation = 0 },
		"overflow-generation": func(c *rc.Context) { c.Generation = 9007199254740992 }, "empty-run": func(c *rc.Context) { c.RunID = "" },
		"status": func(c *rc.Context) { c.Status = "running" }, "empty-revision": func(c *rc.Context) { c.ProvisioningRevision = "" },
		"issued-after-valid": func(c *rc.Context) { c.IssuedAt = now.Add(time.Minute) },
		"expired":            func(c *rc.Context) { c.ExpiresAt = now }, "first-generation-not-one": func(c *rc.Context) { c.Generation = 2 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ledger, err := rc.Initialize(filepath.Join(t.TempDir(), "ledger.json"), scope, public)
			if err != nil {
				t.Fatal(err)
			}
			c := base
			mutate(&c)
			if err = ledger.Commit(signed(t, private, c), now); err == nil {
				t.Fatal("accepted invalid context")
			}
			if got := ledger.Evaluate("rev-a", now, now); got.Status != "absent" {
				t.Fatalf("invalid commit changed state: %+v", got)
			}
		})
	}
	t.Run("signature-and-history", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ledger.json")
		ledger, err := rc.Initialize(path, scope, public)
		if err != nil {
			t.Fatal(err)
		}
		envelope := signed(t, private, base)
		envelope.Signature[0] ^= 1
		if err = ledger.Commit(envelope, now); err == nil {
			t.Fatal("accepted wrong signature")
		}
		if err = ledger.Commit(signed(t, private, base), now); err != nil {
			t.Fatal(err)
		}
		if err = ledger.Apply(now); err != nil {
			t.Fatal(err)
		}
		// Reordered JSON keys do not create a different same-generation body.
		raw, _ := json.Marshal(base)
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		raw, _ = json.Marshal(fields)
		if err = ledger.Commit(rc.Envelope{Payload: raw, Signature: ed25519.Sign(private, raw)}, now); err != nil {
			t.Fatal(err)
		}
		c := base
		c.RunID = "run-b"
		if err = ledger.Commit(signed(t, private, c), now); err == nil {
			t.Fatal("accepted same-generation conflict")
		}
		c = base
		c.Generation = 2
		if err = ledger.Commit(signed(t, private, c), now); err != nil {
			t.Fatal(err)
		}
		if err = ledger.Commit(signed(t, private, base), now); err == nil {
			t.Fatal("accepted rollback")
		}
		if _, err = rc.Initialize(path, scope, public); err == nil {
			t.Fatal("reinitialized history")
		}
		if _, err = rc.Open(path, rc.Scope{GatewayID: "other", ClockID: scope.ClockID}, public); err == nil {
			t.Fatal("opened wrong scope")
		}
		if _, err = rc.Open(filepath.Join(t.TempDir(), "missing.json"), scope, public); err == nil {
			t.Fatal("recovery silently initialized")
		}
	})
}

func TestWholeAcquisitionIntervalAndRevocation(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	scope := rc.Scope{GatewayID: "GW-EXAMPLE", ClockID: "urn:clock:test"}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := rc.Context{SchemaVersion: "clock-run-context/2-draft", GatewayID: scope.GatewayID, ClockID: scope.ClockID, ProvisioningRevision: "rev-a", RunID: "run-a", Generation: 1, Status: "active", IssuedAt: now, ValidFrom: now, ExpiresAt: now.Add(time.Hour)}
	ledger, err := rc.Initialize(filepath.Join(t.TempDir(), "ledger.json"), scope, public)
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.Commit(signed(t, private, c), now); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Apply(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, revision string
		start, end     time.Time
		status         string
	}{
		{"before-apply", "rev-a", now, now.Add(2 * time.Second), "unverified"},
		{"before-valid", "rev-a", now.Add(-time.Second), now, "invalid"},
		{"expires-at-end", "rev-a", now.Add(time.Second), c.ExpiresAt, "invalid"},
		{"revision", "rev-b", now.Add(time.Second), now.Add(2 * time.Second), "invalid"},
		{"reverse", "rev-a", now.Add(2 * time.Second), now.Add(time.Second), "invalid"},
		{"good", "rev-a", now.Add(time.Second), now.Add(2 * time.Second), "valid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ledger.Evaluate(tc.revision, tc.start, tc.end)
			if got.Status != tc.status {
				t.Fatalf("%+v", got)
			}
			if got.Status != "valid" && (got.RunID != "" || got.Generation != 0) {
				t.Fatal("invalid run claim leaked")
			}
		})
	}
	c.Generation = 2
	c.Status = "revoked"
	if err = ledger.Commit(signed(t, private, c), now); err != nil {
		t.Fatal(err)
	}
	if got := ledger.Evaluate("rev-a", now, now); got.Status != "invalid" {
		t.Fatalf("revocation: %+v", got)
	}
	c.Generation = 3
	c.Status = "active"
	if err = ledger.Commit(signed(t, private, c), now); err != nil {
		t.Fatal(err)
	}
	if err = ledger.Apply(now); err != nil {
		t.Fatal(err)
	}
	if got := ledger.Evaluate("rev-a", now, now); got.Status != "invalid" {
		t.Fatalf("revoked run revived: %+v", got)
	}
}
