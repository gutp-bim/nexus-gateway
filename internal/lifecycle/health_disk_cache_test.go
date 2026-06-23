// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"sync/atomic"
	"testing"
	"time"
)

// Disk stats are sampled at most once per TTL, so a burst of /health probes does
// not issue a statfs each time (the cost #72 removes from the hot path).
func TestRuntimeMetrics_DiskCachedWithinTTL(t *testing.T) {
	var calls int32
	fn := func() (float64, float64) { atomic.AddInt32(&calls, 1); return 11, 22 }
	m := newRuntimeMetrics(fn, time.Hour)

	for range 50 {
		s := m.Sample()
		if s.DiskUsedMB != 11 || s.DiskTotalMB != 22 {
			t.Fatalf("disk stats = %v/%v, want 11/22 (cached value surfaced)", s.DiskUsedMB, s.DiskTotalMB)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("statfs called %d times across 50 Sample() calls, want 1 (cached within TTL)", got)
	}
}

// Once the TTL lapses, the next Sample refreshes the disk stats.
func TestRuntimeMetrics_DiskRefreshesAfterTTL(t *testing.T) {
	var calls int32
	fn := func() (float64, float64) { atomic.AddInt32(&calls, 1); return 1, 2 }
	m := newRuntimeMetrics(fn, time.Millisecond)

	m.Sample()
	time.Sleep(5 * time.Millisecond)
	m.Sample()

	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("statfs called %d times, want >= 2 after the TTL lapsed", got)
	}
}
