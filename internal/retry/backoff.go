// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

// Backoff implements truncated exponential backoff with proportional jitter (±20%).
// Zero value is not usable — initialize Min, Max, and Factor before calling Next.
type Backoff struct {
	Min    time.Duration
	Max    time.Duration
	Factor float64
	cur    time.Duration
}

// Next returns the next wait duration and advances the internal state.
// Jitter of ±20% is applied to the base delay before clamping to Max.
func (b *Backoff) Next() time.Duration {
	if b.cur == 0 {
		b.cur = b.Min
	}
	base := b.cur
	jitter := time.Duration(float64(base) * 0.2 * (rand.Float64()*2 - 1))
	d := base + jitter
	if d < b.Min {
		d = b.Min
	}
	if d > b.Max {
		d = b.Max
	}

	b.cur = time.Duration(float64(b.cur) * b.Factor)
	if b.cur > b.Max {
		b.cur = b.Max
	}
	return d
}

// Reset resets the delay to the initial minimum. Call after a successful session.
func (b *Backoff) Reset() { b.cur = 0 }

// Wait sleeps for the next backoff duration or returns early if ctx is cancelled.
// Returns nil when the delay elapsed, or ctx.Err() if the context was cancelled.
func (b *Backoff) Wait(ctx context.Context) error {
	select {
	case <-time.After(b.Next()):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
