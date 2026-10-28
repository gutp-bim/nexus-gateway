// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
package clocksync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/provisioning"
	rc "nexus-gateway/internal/runcontext"
)

func TestGatewayRunContextBindsAcquisitionAndReplaysOriginalFrame(t *testing.T) {
	g, c, _, src, js, _, _, _ := liveFixture(t)
	enableTestRun(t, g, src.s)
	clock := src.s.Native().Clocks[0].ClockID
	require.NoError(t, c.Sync(t.Context()))
	record := g.producer.record
	native := record.snapshot.Native()
	start := time.Now().UTC()
	end := start.Add(time.Microsecond)
	attrs := map[string]string{"sim_clock_id": clock, "sim_config_revision": native.Revision, "sim_run_status": "absent", "sim_time_quality": "stable_unfenced", "sim_time_s": "120.5"}
	body, _ := json.Marshal(map[string]any{"protocol": "bacnet", "connector_id": scope.ConnectorID, "local_id": native.Points[0].LocalID, "device_ref": native.Points[0].DeviceRef, "value": 21.5, "quality": "Good", "timestamp": start.Format(time.RFC3339Nano), "attributes": attrs})
	msg := &nats.Msg{Subject: "evt.bacnet." + scope.ConnectorID, Header: nats.Header{"Nexus-Session": {record.Session}, "Nexus-Config-Revision": {native.Revision}, "Nexus-Poll-Started-At": {start.Format(time.RFC3339Nano)}, "Nexus-Poll-Ended-At": {end.Format(time.RFC3339Nano)}}, Data: body}
	_, err := js.PublishMsg(t.Context(), msg)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return g.buffer.Written() == 1 }, time.Second, 10*time.Millisecond)
	batch, err := g.buffer.ReadBatch(0, 10)
	require.NoError(t, err)
	require.Equal(t, "valid", batch[0].Frame.Attributes["sim_run_status"])
	require.Equal(t, "run-test", batch[0].Frame.Attributes["sim_run_id"])
	require.Equal(t, "1", batch[0].Frame.Attributes["sim_context_generation"])
	require.Equal(t, start.Format(time.RFC3339Nano), batch[0].Frame.Timestamp)
	// Once classified, later reset/replay must preserve the durable frame.
	require.NoError(t, g.run.ledger.InvalidateRun("run-test"))
	_, err = js.PublishMsg(t.Context(), msg)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return g.buffer.Written() == 2 }, time.Second, 10*time.Millisecond)
	batch, err = g.buffer.ReadBatch(0, 10)
	require.NoError(t, err)
	require.Equal(t, batch[0].Frame, batch[1].Frame)
	fresh := time.Now().UTC()
	msg.Header.Set("Nexus-Poll-Started-At", fresh.Format(time.RFC3339Nano))
	msg.Header.Set("Nexus-Poll-Ended-At", fresh.Format(time.RFC3339Nano))
	var data map[string]any
	require.NoError(t, json.Unmarshal(msg.Data, &data))
	data["timestamp"] = fresh.Format(time.RFC3339Nano)
	msg.Data, _ = json.Marshal(data)
	_, err = js.PublishMsg(t.Context(), msg)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return g.buffer.Written() == 3 }, time.Second, 10*time.Millisecond)
	batch, err = g.buffer.ReadBatch(0, 10)
	require.NoError(t, err)
	require.Equal(t, "invalid", batch[2].Frame.Attributes["sim_run_status"])
	require.Equal(t, "reset_detected", batch[2].Frame.Attributes["sim_run_reason"])
	require.NotContains(t, batch[2].Frame.Attributes, "sim_run_id")
	msg.Header.Del("Nexus-Poll-Started-At")
	_, err = js.PublishMsg(t.Context(), msg)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		paths, _ := filepath.Glob(filepath.Join(g.store.dir, "quarantine", "*.json"))
		return len(paths) == 1
	}, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 3, g.buffer.Written())
	_, err = openSessions(g.store.dir, scope)
	require.NoError(t, err)

}

func enableTestRun(t *testing.T, g *Gateway, snapshot *provisioning.ClockSnapshot) {
	t.Helper()
	clock := snapshot.Native().Clocks[0].ClockID
	directory := t.TempDir()
	ledgerPath := filepath.Join(directory, "ledger.json")
	_, err := rc.Initialize(ledgerPath, rc.Scope{GatewayID: scope.GatewayID, ClockID: clock})
	require.NoError(t, err)
	now := time.Now().UTC()
	payload, err := json.Marshal(rc.Context{SchemaVersion: "clock-run-context/2-draft", GatewayID: scope.GatewayID, ClockID: clock, ProvisioningRevision: snapshot.Revision(), RunID: "run-test", Generation: 1, Status: "active", IssuedAt: now.Add(-time.Second), ValidFrom: now.Add(-time.Second), ExpiresAt: now.Add(time.Hour)})
	require.NoError(t, err)
	envelope, _ := json.Marshal(rc.Envelope{Payload: payload})
	envelopePath := filepath.Join(directory, "envelope.json")
	require.NoError(t, os.WriteFile(envelopePath, envelope, 0600))
	cfg, _ := json.Marshal(RunContextConfig{ClockID: clock, EnvelopeFile: envelopePath, LedgerFile: ledgerPath})
	configPath := filepath.Join(directory, "config.json")
	require.NoError(t, os.WriteFile(configPath, cfg, 0600))
	require.NoError(t, g.EnableRunContextFile(configPath))

}

func TestGatewayActualPythonRunContext(t *testing.T) {
	g, c, _, src, _, _, _, _ := liveFixtureAuthenticated(t, true, true)
	enableTestRun(t, g, src.s)
	require.NoError(t, c.Sync(t.Context()))
	require.Eventually(t, func() bool { return g.buffer.Written() >= int64(len(src.s.Entries())) }, 5*time.Second, 10*time.Millisecond)
	frames, err := g.buffer.ReadBatch(0, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(frames), len(src.s.Entries()))
	for _, row := range frames {
		require.Equal(t, "valid", row.Frame.Attributes["sim_run_status"])
		require.Equal(t, "run-test", row.Frame.Attributes["sim_run_id"])
		require.Equal(t, "1", row.Frame.Attributes["sim_context_generation"])
		require.Equal(t, "stable_unfenced", row.Frame.Attributes["sim_time_quality"])
		require.NotContains(t, row.Frame.Attributes, "sim_context_payload")
		require.NotContains(t, row.Frame.Attributes, "sim_context_signature")
		start, err := time.Parse(time.RFC3339Nano, row.Frame.Attributes["sim_poll_started_at"])
		require.NoError(t, err)
		end, err := time.Parse(time.RFC3339Nano, row.Frame.Attributes["sim_poll_ended_at"])
		require.NoError(t, err)
		applied, err := time.Parse(time.RFC3339Nano, row.Frame.Attributes["sim_context_applied_at"])
		require.NoError(t, err)
		observed, err := time.Parse(time.RFC3339Nano, row.Frame.Timestamp)
		require.NoError(t, err)
		require.False(t, start.Before(applied))
		require.False(t, observed.Before(start))
		require.False(t, observed.After(end))

	}
}
