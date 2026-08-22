// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqttsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"nexus-gateway/connector/sdk"
)

// Diff is the result of comparing a connector's currently applied
// subscription state against the Point-List-derived desired state (#119).
type Diff struct {
	Added           []sdk.SubscriptionSpec `json:"added,omitempty"`
	Changed         []sdk.SubscriptionSpec `json:"changed,omitempty"`
	Removed         []sdk.SubscriptionSpec `json:"removed,omitempty"`
	CurrentRevision string                 `json:"current_revision"`
	TargetRevision  string                 `json:"target_revision"`
	SubscribedCount int                    `json:"subscribed_count"`
}

// computeDiff compares current against desired by topic. Whether an add or
// remove actually needs a broker Subscribe/Unsubscribe call (e.g. a topic
// already covered by a static MQTT_SUBSCRIPTIONS wildcard filter) is the
// connector's own responsibility (connector/mqtt, docs/adr/0008) — this diff
// only reasons about the topic set the gateway wants synced.
func computeDiff(current, desired []sdk.SubscriptionSpec) Diff {
	currentByTopic := make(map[string]sdk.SubscriptionSpec, len(current))
	for _, s := range current {
		currentByTopic[s.Topic] = s
	}
	desiredByTopic := make(map[string]sdk.SubscriptionSpec, len(desired))
	for _, s := range desired {
		desiredByTopic[s.Topic] = s
	}

	var d Diff
	for topic, want := range desiredByTopic {
		if have, ok := currentByTopic[topic]; !ok {
			d.Added = append(d.Added, want)
		} else if have != want {
			d.Changed = append(d.Changed, want)
		}
	}
	for topic, have := range currentByTopic {
		if _, ok := desiredByTopic[topic]; !ok {
			d.Removed = append(d.Removed, have)
		}
	}
	d.SubscribedCount = len(desired)
	sortSpecs(d.Added)
	sortSpecs(d.Changed)
	sortSpecs(d.Removed)
	return d
}

func sortSpecs(specs []sdk.SubscriptionSpec) {
	sort.Slice(specs, func(i, j int) bool { return specs[i].Topic < specs[j].Topic })
}

// revisionOf derives a content-addressed revision string for a desired
// subscription set — the same "sha256:..." vocabulary Building OS uses for
// its Point List ETag, so a change in what should be synced always produces a
// different revision (#119 acceptance: "expose the applied revision").
func revisionOf(specs []sdk.SubscriptionSpec) string {
	sorted := append([]sdk.SubscriptionSpec(nil), specs...)
	sortSpecs(sorted)
	data, err := json.Marshal(sorted)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
