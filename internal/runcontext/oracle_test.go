// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
package runcontext_test

import (
	"bytes"
	"encoding/json"
	rc "nexus-gateway/internal/runcontext"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Test the actual unsigned receiver against the unchanged pinned RI oracle.
// Receiver booleans are oracle evidence from this test, never telemetry claims.
func TestRIOracleCompatibility(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("RI offline oracle requires python3")
	}
	scope := rc.Scope{GatewayID: "GW-EXAMPLE", ClockID: "urn:clock:test"}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	context := rc.Context{SchemaVersion: "clock-run-context/2-draft", GatewayID: scope.GatewayID, ClockID: scope.ClockID, ProvisioningRevision: "rev-a", RunID: "run-a", Generation: 1, Status: "active", IssuedAt: now, ValidFrom: now, ExpiresAt: now.Add(time.Hour)}
	var cases []any
	for _, name := range []string{"committed", "applied", "before-apply", "expired", "revision", "revoked", "reset"} {
		t.Run(name, func(t *testing.T) {
			ledger, err := rc.Initialize(filepath.Join(t.TempDir(), "ledger.json"), scope)
			if err != nil {
				t.Fatal(err)
			}
			c := context
			if name == "revoked" {
				c.Status = "revoked"
			}
			if err = ledger.Commit(envelopeOf(t, c), now); err != nil {
				t.Fatal(err)
			}
			var applied *time.Time
			start, end := now.Add(2*time.Second), now.Add(3*time.Second)
			revision := "rev-a"
			invalidated := []string{}
			if name != "committed" {
				at := now.Add(time.Second)
				applied = &at
				if err = ledger.Apply(at); err != nil {
					t.Fatal(err)
				}
			}
			if name == "before-apply" {
				start = now
			}
			if name == "expired" {
				end = c.ExpiresAt
			}
			if name == "revision" {
				revision = "rev-b"
			}
			if name == "reset" {
				if err = ledger.InvalidateRun(c.RunID); err != nil {
					t.Fatal(err)
				}
				invalidated = []string{c.RunID}
			}
			cases = append(cases, map[string]any{"name": name, "context": c, "revision": revision, "start": start, "end": end, "applied": applied, "committed": now, "invalidated": invalidated, "result": ledger.Evaluate(revision, start, end)})
		})
	}
	raw, _ := json.Marshal(cases)
	script := `import hashlib,importlib.util,json,pathlib,sys
root=pathlib.Path('../../test/contracts/ri-clock-v2')
manifest=json.loads((root/'UPSTREAM.json').read_text())
p=root/'time_contract.py'
assert hashlib.sha256(p.read_bytes()).hexdigest()==manifest['files']['time_contract.py']
spec=importlib.util.spec_from_file_location('oracle',p);o=importlib.util.module_from_spec(spec);spec.loader.exec_module(o)
for c in json.load(sys.stdin):
 context=c['context']
 acquisition=dict(startedAt=c['start'],endedAt=c['end'],chunkCount=1,clockSamples=['120','120'],resetLatched=False,contextChanged=False)
 receiver=dict(authenticated=True,issuerAuthorized=True,historyRecovered=True,committedContext=context,committedAt=c['committed'],appliedAt=c['applied'],invalidatedRunIds=c['invalidated'])
 status,reason=o.context_state(dict(gatewayId=context['gatewayId'],revision=c['revision']),context['clockId'],context,acquisition,receiver)
 got=c['result'];assert (got['Status'],got['Reason'] or None)==(status,reason),(c['name'],got,(status,reason))
`
	command := exec.CommandContext(t.Context(), python, "-c", script)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	command.Stdin = bytes.NewReader(raw)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("RI oracle mismatch: %v\n%s", err, output)
	}
}
