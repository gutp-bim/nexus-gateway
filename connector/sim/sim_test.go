// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"testing"
	"time"
)

// A non-positive interval must be clamped to a positive default: it flows from
// --interval / SIM_POLL_INTERVAL / --dev-sim-interval into time.NewTicker, which
// panics for any value <= 0. New is the single guard protecting every caller.
func TestNew_ClampsNonPositiveInterval(t *testing.T) {
	for _, in := range []time.Duration{0, -5 * time.Second} {
		c := New("sim-01", nil, in, nil)
		if c.interval <= 0 {
			t.Fatalf("interval %v must be clamped to a positive value, got %v", in, c.interval)
		}
	}
}

// A positive interval must be preserved as-is.
func TestNew_KeepsPositiveInterval(t *testing.T) {
	c := New("sim-01", nil, 5*time.Second, nil)
	if c.interval != 5*time.Second {
		t.Fatalf("expected interval 5s preserved, got %v", c.interval)
	}
}
