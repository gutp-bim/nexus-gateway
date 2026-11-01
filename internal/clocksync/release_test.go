// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package clocksync

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/common"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
)

// The RI snapshot with an RI #600 release point (BPT-0033-REL -> BPT-0033) has a
// release entry without LocalID. It must neither bind telemetry nor shadow its
// target, while the dispatcher can still resolve the release relation.
func releaseSnapshot(t *testing.T) *provisioning.ClockSnapshot {
	t.Helper()
	data, err := os.ReadFile("../provisioning/testdata/clock-provisioning-release.json")
	require.NoError(t, err)
	s, err := provisioning.StageClock(data, scope)
	require.NoError(t, err)
	return s
}

func TestReleaseEntryDoesNotBindTelemetry(t *testing.T) {
	s := releaseSnapshot(t)
	g := &Gateway{store: &sessions{scope: scope}}
	r := &sessionRecord{snapshot: s}
	attrs := func() map[string]string {
		return map[string]string{"sim_clock_id": s.Native().Clocks[0].ClockID, "sim_config_revision": s.Revision(), "sim_run_status": "absent", "sim_time_quality": "stable_unfenced", "sim_time_s": "120.5"}
	}
	evt := common.Event{Protocol: "bacnet", ConnectorID: scope.ConnectorID, LocalID: "analogOutput,34", DeviceRef: "599", Value: common.NumberValue(21.5), Quality: "Good", Timestamp: "2026-11-01T00:00:00.125000Z", Attributes: attrs()}
	frame, ok := g.boundFrame(evt, r)
	require.True(t, ok)
	require.Equal(t, "BPT-0033", frame.PointId)

	// An event that matches the release entry's empty LocalID and its DeviceRef is
	// not a point of this snapshot, with or without clock attributes.
	for _, a := range []map[string]string{nil, attrs()} {
		evt = common.Event{Protocol: "bacnet", ConnectorID: scope.ConnectorID, LocalID: "", DeviceRef: "DEV-FCU-1", Value: common.NumberValue(1), Quality: "Good", Timestamp: "2026-11-01T00:00:00.125000Z", Attributes: a}
		_, ok = g.boundFrame(evt, r)
		require.False(t, ok)
	}
}

func TestReleaseEntryResolvesThroughSyncedResolver(t *testing.T) {
	s := releaseSnapshot(t)
	resolver := pointlist.NewSynced(nil)
	resolver.Update(s.Entries())
	id, ok := resolver.Resolve(scope.ConnectorID, "analogOutput,34")
	require.True(t, ok)
	require.Equal(t, "BPT-0033", id)
	_, ok = resolver.Resolve(scope.ConnectorID, "")
	require.False(t, ok)
	entry, target, ok := resolver.ResolveControl("BPT-0033-REL")
	require.True(t, ok)
	require.Equal(t, "BPT-0033", entry.ReleaseOf)
	require.Equal(t, "BPT-0033", target.PointID)
	require.Equal(t, "analogOutput,34", target.LocalID)
	require.Equal(t, scope.ConnectorID, target.ConnectorID)
}
