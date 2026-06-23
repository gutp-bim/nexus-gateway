// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package sdk_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/connector/sdk"
)

// TestCommandDedup_FirstCallProceeds: new controlID → caller should write.
func TestCommandDedup_FirstCallProceeds(t *testing.T) {
	d := sdk.NewCommandDedup(100)
	proceed, cached := d.TryReserve("ctrl-1")
	assert.True(t, proceed, "first caller for a new controlID must proceed")
	assert.Nil(t, cached, "no cached reply yet")
}

// TestCommandDedup_InFlightReturnsNotProceed: second goroutine for same controlID
// while first is still writing must NOT proceed (in-flight sentinel).
func TestCommandDedup_InFlightReturnsNotProceed(t *testing.T) {
	d := sdk.NewCommandDedup(100)
	proceed1, _ := d.TryReserve("ctrl-1")
	require.True(t, proceed1)

	// Before completing, a second caller for the same controlID.
	proceed2, cached2 := d.TryReserve("ctrl-1")
	assert.False(t, proceed2, "second caller while in-flight must not proceed")
	assert.Nil(t, cached2, "in-flight means no cached result yet")
}

// TestCommandDedup_CachedResultReturned: after Complete, subsequent callers get the cached reply.
func TestCommandDedup_CachedResultReturned(t *testing.T) {
	d := sdk.NewCommandDedup(100)
	proceed, _ := d.TryReserve("ctrl-1")
	require.True(t, proceed)

	reply := sdk.WriteReply{Success: true, Response: "ok"}
	d.Complete("ctrl-1", reply)

	proceed2, cached2 := d.TryReserve("ctrl-1")
	assert.False(t, proceed2, "after completion, no re-write")
	require.NotNil(t, cached2)
	assert.Equal(t, reply, *cached2)
}

// TestCommandDedup_DifferentIDsAreIndependent: two controlIDs don't interfere.
func TestCommandDedup_DifferentIDsAreIndependent(t *testing.T) {
	d := sdk.NewCommandDedup(100)
	p1, _ := d.TryReserve("ctrl-1")
	p2, _ := d.TryReserve("ctrl-2")
	assert.True(t, p1)
	assert.True(t, p2, "different controlIDs must not block each other")
}

// TestCommandDedup_EvictsOldestWhenFull: once cap is reached the oldest entry is evicted
// so the corresponding controlID can be claimed again.
func TestCommandDedup_EvictsOldestWhenFull(t *testing.T) {
	d := sdk.NewCommandDedup(3)
	for i := range 3 {
		id := string(rune('A' + i)) // "A", "B", "C"
		proceed, _ := d.TryReserve(id)
		require.True(t, proceed)
		d.Complete(id, sdk.WriteReply{Success: true})
	}

	// Dedup is full (A, B, C). Adding D must evict A.
	proceed, _ := d.TryReserve("D")
	require.True(t, proceed)

	// "A" was evicted: TryReserve("A") must now treat it as a new controlID.
	pA, _ := d.TryReserve("A")
	assert.True(t, pA, "evicted controlID must be treated as new")
}

// TestCommandDedup_ConcurrentSameID: under concurrent callers only one goroutine proceeds.
func TestCommandDedup_ConcurrentSameID(t *testing.T) {
	d := sdk.NewCommandDedup(100)
	const N = 50
	proceeds := make([]bool, N)
	var wg sync.WaitGroup
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, _ := d.TryReserve("ctrl-concurrent")
			proceeds[i] = p
		}(i)
	}
	wg.Wait()

	count := 0
	for _, p := range proceeds {
		if p {
			count++
		}
	}
	assert.Equal(t, 1, count, "exactly one goroutine must proceed for a concurrent controlID")
}
