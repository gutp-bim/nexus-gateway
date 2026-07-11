// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The freshness floor re-publishes a Point's last-known value only when no broker
// update has arrived within the interval; a fresh update resets the timer, and a
// Point that never reported is never invented (#34).
func TestDueForRepublish(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	c := &Connector{
		cfg: Config{FreshnessInterval: time.Minute},
		lkv: map[string]*lkvState{},
	}

	// p1 last emitted 90s ago → due; p2 last emitted 30s ago → not due.
	c.lkv["p1"] = &lkvState{value: 1, lastEmit: base.Add(-90 * time.Second)}
	c.lkv["p2"] = &lkvState{value: 2, lastEmit: base.Add(-30 * time.Second)}

	due := c.dueForRepublish(base)
	assert.ElementsMatch(t, []string{"p1"}, due, "only the idle-past-interval point is due")

	// A broker update on p1 (lastEmit advanced to now) resets its timer → not due.
	c.lkv["p1"].lastEmit = base
	assert.Empty(t, c.dueForRepublish(base), "a fresh update resets the floor timer")

	// A never-reported configured point (absent from lkv) is never invented.
	assert.NotContains(t, c.dueForRepublish(base.Add(time.Hour)), "p-never")
}

// A zero (or negative) interval disables the freshness floor entirely.
func TestDueForRepublish_DisabledWhenIntervalZero(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	c := &Connector{
		cfg: Config{FreshnessInterval: 0},
		lkv: map[string]*lkvState{"p1": {value: 1, lastEmit: base.Add(-time.Hour)}},
	}
	assert.Empty(t, c.dueForRepublish(base), "interval 0 → floor disabled, nothing due")
}
