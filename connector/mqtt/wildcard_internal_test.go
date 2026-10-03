// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func pts(topics ...string) []PointConfig {
	out := make([]PointConfig, len(topics))
	for i, topic := range topics {
		out[i] = PointConfig{Topic: topic, Unit: "u:" + topic}
	}
	return out
}

// An exact configuration always beats a wildcard Point that also matches (#173).
func TestResolve_ExactBeatsWildcard(t *testing.T) {
	idx := newTopicIndex(pts("#", "sensors/exact"), nil, "")

	p, explicit, ok := idx.resolve("sensors/exact")
	assert.True(t, ok)
	assert.True(t, explicit)
	assert.Equal(t, "sensors/exact", p.Topic)

	p, explicit, ok = idx.resolve("sensors/other")
	assert.True(t, ok)
	assert.False(t, explicit, "matched only through the wildcard")
	assert.Equal(t, "#", p.Topic, "wildcard supplies the metadata; the caller emits the concrete topic")
}

// With several matching wildcards the most specific (most literal levels) wins,
// whatever order the points arrive in — the live-apply path flattens a map.
func TestResolve_MostSpecificWildcardWinsRegardlessOfOrder(t *testing.T) {
	topics := []string{"#", "sensors/#", "sensors/+/temp", "+/+/temp"}
	for shift := range topics {
		rotated := append(append([]string{}, topics[shift:]...), topics[:shift]...)
		idx := newTopicIndex(pts(rotated...), nil, "")
		p, _, ok := idx.resolve("sensors/a/temp")
		assert.True(t, ok)
		assert.Equal(t, "sensors/+/temp", p.Topic, "input order %v", rotated)
	}
}

func TestResolve_NoMatch(t *testing.T) {
	idx := newTopicIndex(pts("sensors/+/temp", "plant/#"), nil, "")
	_, _, ok := idx.resolve("sensors/a/humidity")
	assert.False(t, ok)

	// "#" alone never matches a $-prefixed topic (MQTT 5 §4.7.2).
	idx = newTopicIndex(pts("#"), nil, "")
	_, _, ok = idx.resolve("$SYS/broker/uptime")
	assert.False(t, ok)
}

func TestAllowWildcard_SpacesMessagesPerTopic(t *testing.T) {
	c := &Connector{cfg: Config{WildcardMinInterval: 10 * time.Second}, wildLast: map[string]time.Time{}}
	t0 := time.Unix(1_700_000_000, 0)

	assert.True(t, c.allowWildcard("a", t0))
	assert.False(t, c.allowWildcard("a", t0.Add(time.Second)), "1 s publisher is dropped")
	assert.False(t, c.allowWildcard("a", t0.Add(9*time.Second)))
	assert.True(t, c.allowWildcard("b", t0.Add(time.Second)), "limit is per topic")
	assert.True(t, c.allowWildcard("a", t0.Add(10*time.Second)), "allowed again after the interval")
}

func TestAllowWildcard_DefaultAndDisabled(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)

	def := &Connector{wildLast: map[string]time.Time{}} // zero value → default 10 s
	assert.True(t, def.allowWildcard("a", t0))
	assert.False(t, def.allowWildcard("a", t0.Add(5*time.Second)))
	assert.True(t, def.allowWildcard("a", t0.Add(defaultWildcardMinInterval)))

	off := &Connector{cfg: Config{WildcardMinInterval: -1}, wildLast: map[string]time.Time{}}
	for range 5 {
		assert.True(t, off.allowWildcard("a", t0), "negative interval disables the limit")
	}
}

// The limiter's table is bounded: a flood of distinct topics cannot grow it forever.
func TestAllowWildcard_TableIsBounded(t *testing.T) {
	c := &Connector{cfg: Config{WildcardMinInterval: time.Hour}, wildLast: map[string]time.Time{}}
	t0 := time.Unix(1_700_000_000, 0)
	for i := range maxWildcardTracked {
		c.wildLast[fmt.Sprintf("t/%d", i)] = t0 // all still inside the interval
	}

	assert.True(t, c.allowWildcard("fresh", t0.Add(time.Second)))
	assert.LessOrEqual(t, len(c.wildLast), maxWildcardTracked)
	assert.Contains(t, c.wildLast, "fresh")
}
