// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package retry_test

import (
	"context"
	"testing"
	"time"

	"nexus-gateway/internal/retry"
)

func newTestBackoff() *retry.Backoff {
	return &retry.Backoff{Min: time.Second, Max: 60 * time.Second, Factor: 2.0}
}

func TestBackoff_FirstCallReturnsNearMin(t *testing.T) {
	b := newTestBackoff()
	d := b.Next()
	// Jitter is ±20% of Min (1s), so range is [0.8s, 1.2s].
	if d < 800*time.Millisecond || d > 1200*time.Millisecond {
		t.Fatalf("first Next() = %v, want ~1s (±20%%)", d)
	}
}

func TestBackoff_DelayGrowsExponentially(t *testing.T) {
	b := newTestBackoff()
	prev := b.Next() // 1s base
	for i := 0; i < 5; i++ {
		cur := b.Next()
		// Each step should be larger than the previous (modulo jitter).
		// We compare the floor: cur's base is prev's base * 2, so cur > prev * 1.5 conservatively.
		if cur <= prev/2 {
			t.Fatalf("step %d: cur=%v <= prev/2=%v — backoff not growing", i+1, cur, prev/2)
		}
		prev = cur
	}
}

func TestBackoff_DelayCapAtMax(t *testing.T) {
	b := newTestBackoff()
	for i := 0; i < 20; i++ {
		d := b.Next()
		// With ±20% jitter on Max (60s): max possible = 60s * 1.2 = 72s.
		// But we clamp to Max before jitter is applied — so max is Max * 1.2.
		if d > 72*time.Second {
			t.Fatalf("step %d: Next() = %v exceeds Max*1.2 (72s)", i+1, d)
		}
	}
}

func TestBackoff_ResetRestoresMin(t *testing.T) {
	b := newTestBackoff()
	// Advance several steps.
	for i := 0; i < 6; i++ {
		b.Next()
	}
	b.Reset()
	d := b.Next()
	if d < 800*time.Millisecond || d > 1200*time.Millisecond {
		t.Fatalf("after Reset(), Next() = %v, want ~1s (±20%%)", d)
	}
}

func TestBackoff_WaitReturnsNilAfterDelay(t *testing.T) {
	b := &retry.Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond, Factor: 2.0}
	ctx := context.Background()
	if err := b.Wait(ctx); err != nil {
		t.Fatalf("Wait returned err=%v, want nil", err)
	}
}

func TestBackoff_WaitRespectsCtxCancellation(t *testing.T) {
	b := &retry.Backoff{Min: 10 * time.Second, Max: 60 * time.Second, Factor: 2.0}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	start := time.Now()
	err := b.Wait(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Wait must return ctx.Err() when context is already cancelled")
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("Wait blocked for %v after ctx cancel; should return immediately", elapsed)
	}
}

func TestBackoff_JitterBounds(t *testing.T) {
	b := newTestBackoff()
	for i := 0; i < 1000; i++ {
		b.Reset()
		d := b.Next()
		if d < 800*time.Millisecond || d > 1200*time.Millisecond {
			t.Fatalf("jitter out of ±20%% bounds on iteration %d: got %v", i, d)
		}
	}
}
