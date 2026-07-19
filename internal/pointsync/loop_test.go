// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointsync_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/pointsync"
	"nexus-gateway/internal/provisioning"
)

// TestLoop_InitialFetch_LoadsResolver verifies the first sync always calls Fetch
// with empty knownETag and updates the resolver.
func TestLoop_InitialFetch_LoadsResolver(t *testing.T) {
	mock := provisioning.NewMock([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})

	resolver := pointlist.NewSynced(nil)
	cfg := pointsync.Config{Interval: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	loop := pointsync.New(mock, resolver, cfg)
	loop.Run(ctx)

	pid, ok := resolver.Resolve("c1", "l1")
	require.True(t, ok, "resolver must be populated after first sync")
	assert.Equal(t, "p1", pid)
}

// TestLoop_AppliedRevision_TracksETag verifies AppliedRevision() is empty before any sync and
// equals the applied ETag afterwards (#230 Phase 2b — the egress agent reports it up the stream).
func TestLoop_AppliedRevision_TracksETag(t *testing.T) {
	mock := provisioning.NewMock([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})
	resolver := pointlist.NewSynced(nil)
	cfg := pointsync.Config{Interval: 100 * time.Millisecond}

	loop := pointsync.New(mock, resolver, cfg)
	assert.Empty(t, loop.AppliedRevision(), "no revision before the first sync")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	loop.Run(ctx)

	assert.Equal(t, mock.CurrentETag(), loop.AppliedRevision(), "revision must match the applied ETag")
}

// TestLoop_NoUpdateWhenETagUnchanged verifies the resolver is not touched when
// Fetch returns nil (304 — ETag unchanged between polls).
func TestLoop_NoUpdateWhenETagUnchanged(t *testing.T) {
	mock := provisioning.NewMock([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})
	resolver := pointlist.NewSynced(nil)
	cfg := pointsync.Config{Interval: 20 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	pointsync.New(mock, resolver, cfg).Run(ctx)

	// First call fetches (empty ETag → full), subsequent calls get 304 (nil).
	// Total Fetch calls ≥ 2, but resolver still has the original entries.
	assert.GreaterOrEqual(t, mock.FetchCalls(), 2, "must poll multiple times")
	pid, ok := resolver.Resolve("c1", "l1")
	require.True(t, ok)
	assert.Equal(t, "p1", pid)
}

// TestLoop_UpdatesResolverOnETagChange verifies reconvergence when ETag changes.
func TestLoop_UpdatesResolverOnETagChange(t *testing.T) {
	mock := provisioning.NewMock([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})
	resolver := pointlist.NewSynced(nil)
	cfg := pointsync.Config{Interval: 20 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go pointsync.New(mock, resolver, cfg).Run(ctx)

	require.Eventually(t, func() bool {
		_, ok := resolver.Resolve("c1", "l1")
		return ok
	}, 200*time.Millisecond, 5*time.Millisecond, "initial mapping must load")

	mock.SetEntries([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1-updated"},
	})

	require.Eventually(t, func() bool {
		pid, _ := resolver.Resolve("c1", "l1")
		return pid == "p1-updated"
	}, 300*time.Millisecond, 5*time.Millisecond, "resolver must reconverge after ETag change")
}

// TestLoop_AppliesDiff verifies that a delta (Added/Removed/Changed) is correctly
// merged into the resolver. The mock returns the initial full list on the first Fetch,
// then the diff on the second Fetch — mirroring the real #224 poll sequence.
func TestLoop_AppliesDiff(t *testing.T) {
	initial := []pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l2", PointID: "p2"},
	}
	diff := &provisioning.FetchResult{
		ETag: "etag-v2",
		Full: false,
		Added: []pointlist.Entry{
			{ConnectorID: "c1", Protocol: "sim", LocalID: "l3", PointID: "p3"},
		},
		Removed: []string{"p2"},
		Changed: []pointlist.Entry{
			{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1-renamed"},
		},
	}
	// two-stage mock: first Fetch → full initial; second Fetch → diff; subsequent → nil.
	m := &twoStageMock{initialEntries: initial, initialETag: "etag-v1", diffResult: diff}

	resolver := pointlist.NewSynced(nil)
	// Use a short interval so the loop polls at least twice within the timeout.
	cfg := pointsync.Config{Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	go pointsync.New(m, resolver, cfg).Run(ctx)

	// Wait until the diff has been applied (p3 is the newly added point).
	require.Eventually(t, func() bool {
		_, ok := resolver.Resolve("c1", "l3")
		return ok
	}, 250*time.Millisecond, 5*time.Millisecond, "diff must be applied: l3/p3 must appear")

	// p3 added
	pid, ok := resolver.Resolve("c1", "l3")
	require.True(t, ok)
	assert.Equal(t, "p3", pid)

	// p2 removed
	_, ok = resolver.ResolveReverse("p2")
	assert.False(t, ok, "removed point p2 must not be in resolver")

	// p1 changed (l1 now maps to p1-renamed)
	pid, ok = resolver.Resolve("c1", "l1")
	require.True(t, ok, "changed entry l1 must still be in resolver")
	assert.Equal(t, "p1-renamed", pid)
}

// TestLoop_ReadyCloses_AfterFirstSync verifies that Ready() closes as soon as the
// first Fetch completes successfully, so callers can wait on a channel instead of
// polling resolver.Snapshot().
func TestLoop_ReadyCloses_AfterFirstSync(t *testing.T) {
	mock := provisioning.NewMock([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})
	resolver := pointlist.NewSynced(nil)
	cfg := pointsync.Config{Interval: time.Minute} // long interval; only first sync matters

	loop := pointsync.New(mock, resolver, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go loop.Run(ctx)

	select {
	case <-loop.Ready():
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("Ready() did not close within 3s after first sync")
	}

	pid, ok := resolver.Resolve("c1", "l1")
	require.True(t, ok, "resolver must be populated when Ready() closes")
	assert.Equal(t, "p1", pid)
}

// TestLoop_ReadyCloses_AfterFirstSync_Error verifies that Ready() closes even when
// the first Fetch returns an error, so callers can detect the timeout via time.After
// rather than waiting indefinitely.
func TestLoop_ReadyCloses_AfterFirstSync_Error(t *testing.T) {
	errMock := &alwaysErrorMock{}
	resolver := pointlist.NewSynced(nil)
	cfg := pointsync.Config{Interval: time.Minute}

	loop := pointsync.New(errMock, resolver, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go loop.Run(ctx)

	select {
	case <-loop.Ready():
		// closed fast — resolver is empty but caller decides what to do
	case <-time.After(3 * time.Second):
		t.Fatal("Ready() must close even after a failed first sync")
	}

	assert.Empty(t, resolver.Snapshot(), "resolver must be empty after failed first sync")
}

// alwaysErrorMock is a provisioning.Client that always returns an error.
type alwaysErrorMock struct{}

func (m *alwaysErrorMock) Fetch(_ context.Context, _ string) (*provisioning.FetchResult, error) {
	return nil, fmt.Errorf("network unavailable")
}

// TestLoop_PersistsAndLoadsOnRestart verifies that synced state survives restart.
func TestLoop_PersistsAndLoadsOnRestart(t *testing.T) {
	path := t.TempDir() + "/pl.json"
	mock := provisioning.NewMock([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})

	// First run: load and persist.
	r1 := pointlist.NewSynced(nil)
	cfg := pointsync.Config{Interval: 20 * time.Millisecond, PersistPath: path}
	ctx1, cancel1 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel1()
	pointsync.New(mock, r1, cfg).Run(ctx1)

	pid, ok := r1.Resolve("c1", "l1")
	require.True(t, ok)
	assert.Equal(t, "p1", pid)

	// Simulate restart: new resolver, load persisted file.
	r2 := pointlist.NewSynced(nil)
	require.NoError(t, r2.Load(path))
	pid2, ok2 := r2.Resolve("c1", "l1")
	require.True(t, ok2)
	assert.Equal(t, "p1", pid2)
}

// TestLoop_RevalidatesOnPush verifies that a push signal triggers an immediate sync.
func TestLoop_RevalidatesOnPush(t *testing.T) {
	initial := []pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	}
	mock := provisioning.NewMock(initial)
	resolver := pointlist.NewSynced(nil)

	// Use a very long interval so only the push triggers a re-sync.
	revalidate := make(chan struct{}, 1)
	cfg := pointsync.Config{Interval: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go pointsync.New(mock, resolver, cfg).WithRevalidate(revalidate).Run(ctx)

	// Wait for initial sync.
	require.Eventually(t, func() bool {
		_, ok := resolver.Resolve("c1", "l1")
		return ok
	}, 200*time.Millisecond, 5*time.Millisecond, "initial mapping must load")

	// Update entries and send push signal.
	mock.SetEntries([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1-pushed"},
	})
	revalidate <- struct{}{}

	require.Eventually(t, func() bool {
		pid, _ := resolver.Resolve("c1", "l1")
		return pid == "p1-pushed"
	}, 300*time.Millisecond, 5*time.Millisecond, "push signal must trigger immediate re-sync")
}

// ── helpers ─────────────────────────────────────────────────────────────────

// twoStageMock simulates the real #224 poll sequence:
//   call 1 (knownETag=""): full initial list
//   call 2 (knownETag=initialETag): diff result
//   call 3+: nil (304 — unchanged)
type twoStageMock struct {
	initialEntries []pointlist.Entry
	initialETag    string
	diffResult     *provisioning.FetchResult
	calls          int
}

func (m *twoStageMock) Fetch(_ context.Context, _ string) (*provisioning.FetchResult, error) {
	m.calls++
	switch m.calls {
	case 1:
		return &provisioning.FetchResult{ETag: m.initialETag, Full: true, Entries: m.initialEntries}, nil
	case 2:
		return m.diffResult, nil
	default:
		return nil, nil
	}
}
