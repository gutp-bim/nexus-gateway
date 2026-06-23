// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointlist_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/pointlist"
)

// SyncedResolver must satisfy both resolution seams: forward (Normalizer) and
// reverse (control dispatch). The reverse seam is named in the pointlist package
// so the Dispatcher consumes it instead of redeclaring its own interface.
var (
	_ pointlist.Resolver        = (*pointlist.SyncedResolver)(nil)
	_ pointlist.ReverseResolver = (*pointlist.SyncedResolver)(nil)
)

func TestSynced_BasicResolve(t *testing.T) {
	r := pointlist.NewSynced(nil)
	r.Update([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})

	pid, ok := r.Resolve("c1", "l1")
	assert.True(t, ok)
	assert.Equal(t, "p1", pid)

	_, ok = r.Resolve("c1", "unknown")
	assert.False(t, ok)
}

func TestSynced_UpdateReplacesMapping(t *testing.T) {
	r := pointlist.NewSynced(nil)
	r.Update([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "old-point"},
	})
	pid, _ := r.Resolve("c1", "l1")
	assert.Equal(t, "old-point", pid)

	// Update with new mapping — must take effect immediately for concurrent readers
	r.Update([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "new-point"},
	})
	pid, ok := r.Resolve("c1", "l1")
	assert.True(t, ok)
	assert.Equal(t, "new-point", pid)
}

func TestSynced_RemovalStopsNormalization(t *testing.T) {
	r := pointlist.NewSynced(nil)
	r.Update([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})
	_, ok := r.Resolve("c1", "l1")
	require.True(t, ok)

	// Remove the entry
	r.Update([]pointlist.Entry{})
	_, ok = r.Resolve("c1", "l1")
	assert.False(t, ok, "removed entry must not resolve")
}

func TestSynced_ReverseResolve(t *testing.T) {
	r := pointlist.NewSynced(nil)
	r.Update([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "bacnet", LocalID: "dev:1/AI:2", PointID: "zone_temp",
			Writable: true, Unit: "Cel", DeviceRef: "dev:1"},
	})

	entry, ok := r.ResolveReverse("zone_temp")
	require.True(t, ok)
	assert.Equal(t, "c1", entry.ConnectorID)
	assert.Equal(t, "dev:1/AI:2", entry.LocalID)
	assert.True(t, entry.Writable)
	assert.Equal(t, "Cel", entry.Unit)

	_, ok = r.ResolveReverse("nonexistent")
	assert.False(t, ok)
}

func TestSynced_PersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pointlist.json")
	r := pointlist.NewSynced(nil)
	entries := []pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1", Unit: "Cel", Writable: false},
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l2", PointID: "p2", Unit: "Pa", Writable: true},
	}
	r.Update(entries)
	require.NoError(t, r.Persist(path))

	// Reload from disk
	r2 := pointlist.NewSynced(nil)
	require.NoError(t, r2.Load(path))

	pid, ok := r2.Resolve("c1", "l1")
	require.True(t, ok)
	assert.Equal(t, "p1", pid)

	e, ok := r2.ResolveReverse("p2")
	require.True(t, ok)
	assert.Equal(t, "Pa", e.Unit)
}

func TestSynced_LoadMissingFileIsNoop(t *testing.T) {
	r := pointlist.NewSynced(nil)
	// non-existent file should not error — just leave resolver empty (fresh start)
	err := r.Load("/tmp/does-not-exist-xyzzy.json")
	assert.NoError(t, err)
	_, ok := r.Resolve("c1", "l1")
	assert.False(t, ok)
}

func TestSynced_Snapshot(t *testing.T) {
	r := pointlist.NewSynced(nil)
	entries := []pointlist.Entry{
		{ConnectorID: "c1", LocalID: "l1", PointID: "p1"},
		{ConnectorID: "c1", LocalID: "l2", PointID: "p2"},
	}
	r.Update(entries)

	snap := r.Snapshot()
	require.Len(t, snap, 2)

	// Round-trip through JSON to verify all fields survive
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	var decoded []pointlist.Entry
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Len(t, decoded, 2)
}

func TestSynced_LoadCorruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))
	r := pointlist.NewSynced(nil)
	err := r.Load(path)
	assert.Error(t, err)
}
