// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"fmt"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
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

func explicitTopics(idx *topicIndex) []string {
	out := make([]string, 0, len(idx.explicitSubs))
	for topic := range idx.explicitSubs {
		out = append(out, topic)
	}
	return out
}

// An exact point that a wildcard point already reaches gets no Subscribe of its
// own: a broker that delivers once per overlapping subscription would otherwise
// double-count its telemetry (docs/adr/0008, review on #190). The wildcard point
// itself always subscribes, and unrelated exact points still do.
func TestNewTopicIndex_WildcardPointCoversExactPoints(t *testing.T) {
	idx := newTopicIndex(pts("#", "sensors/exact", "plant/#", "plant/ahu1/temp", "other/x"), nil, "")
	// Every exact point is under "#", so none subscribes. Wildcard points are the
	// operator's explicit choice and always subscribe, even where one overlaps
	// another ("plant/#" under "#").
	assert.ElementsMatch(t, []string{"#", "plant/#"}, explicitTopics(idx))

	idx = newTopicIndex(pts("plant/#", "plant/ahu1/temp", "other/x"), nil, "")
	assert.ElementsMatch(t, []string{"plant/#", "other/x"}, explicitTopics(idx),
		"only the topic under plant/# is covered; other/x still subscribes")

	// Routing is unaffected: covered points still resolve (exact first).
	p, explicit, ok := idx.resolve("plant/ahu1/temp")
	assert.True(t, ok)
	assert.True(t, explicit)
	assert.Equal(t, "plant/ahu1/temp", p.Topic)
}

func TestNewTopicIndex_WildcardPointNeverSuppressesItself(t *testing.T) {
	idx := newTopicIndex(pts("sensors/+/temp"), nil, "")
	assert.ElementsMatch(t, []string{"sensors/+/temp"}, explicitTopics(idx))
}

// A static MQTT_SUBSCRIPTIONS filter keeps covering as before.
func TestNewTopicIndex_StaticFilterStillCovers(t *testing.T) {
	idx := newTopicIndex(pts("sensors/a", "other/b"), []SubscriptionConfig{{Filter: "sensors/#"}}, "")
	assert.ElementsMatch(t, []string{"other/b"}, explicitTopics(idx))
}

func topicsOf(opts []paho.SubscribeOptions) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Topic
	}
	return out
}

// planSubscriptions derives the broker calls from the explicit sets, so a wildcard
// point appearing or disappearing moves the exact topics it covers in and out of
// their own subscriptions even though those points did not change.
func TestPlanSubscriptions_TransitionsAroundWildcardPoints(t *testing.T) {
	exact := newTopicIndex(pts("sensors/a", "sensors/b"), nil, "r1")

	// A wildcard point appears: the exact subscriptions become redundant.
	withWildcard := newTopicIndex(pts("sensors/a", "sensors/b", "sensors/#"), nil, "r2")
	sub, unsub := planSubscriptions(exact, withWildcard, nil)
	assert.Equal(t, []string{"sensors/#"}, topicsOf(sub))
	assert.Equal(t, []string{"sensors/a", "sensors/b"}, unsub)

	// It disappears again: the exact topics must be subscribed again, not left
	// uncovered, and the wildcard subscription goes.
	sub, unsub = planSubscriptions(withWildcard, exact, nil)
	assert.Equal(t, []string{"sensors/a", "sensors/b"}, topicsOf(sub))
	assert.Equal(t, []string{"sensors/#"}, unsub)

	// Plain add / remove / change keep working.
	next := newTopicIndex(pts("sensors/a", "sensors/c"), nil, "r3")
	sub, unsub = planSubscriptions(exact, next, nil)
	assert.Equal(t, []string{"sensors/c"}, topicsOf(sub), "only the new topic subscribes")
	assert.Equal(t, []string{"sensors/b"}, unsub)

	sub, unsub = planSubscriptions(exact, exact, []string{"sensors/a"})
	assert.Equal(t, []string{"sensors/a"}, topicsOf(sub), "a changed topic re-subscribes")
	assert.Empty(t, unsub)

	sub, unsub = planSubscriptions(exact, exact, nil)
	assert.Empty(t, sub)
	assert.Empty(t, unsub)
}
