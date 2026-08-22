// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqttsync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/connector/sdk"
)

func topics(specs []sdk.SubscriptionSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Topic
	}
	return out
}

func TestComputeDiff_AddedChangedRemoved(t *testing.T) {
	current := []sdk.SubscriptionSpec{
		{Topic: "sensors/a", QoS: 1, DeviceRef: "dev-a"},
		{Topic: "sensors/b", QoS: 1, DeviceRef: "dev-b"},
	}
	desired := []sdk.SubscriptionSpec{
		{Topic: "sensors/a", QoS: 1, DeviceRef: "dev-a-changed"}, // changed
		{Topic: "sensors/c", QoS: 1, DeviceRef: "dev-c"},         // added
		// sensors/b omitted -> removed
	}

	d := computeDiff(current, desired)
	assert.Equal(t, []string{"sensors/c"}, topics(d.Added))
	assert.Equal(t, []string{"sensors/a"}, topics(d.Changed))
	assert.Equal(t, []string{"sensors/b"}, topics(d.Removed))
	assert.Equal(t, len(desired), d.SubscribedCount)
}

func TestComputeDiff_QoSChangeCountsAsChanged(t *testing.T) {
	current := []sdk.SubscriptionSpec{{Topic: "sensors/a", QoS: 0}}
	desired := []sdk.SubscriptionSpec{{Topic: "sensors/a", QoS: 1}}
	d := computeDiff(current, desired)
	assert.Equal(t, []string{"sensors/a"}, topics(d.Changed))
	assert.Empty(t, d.Added)
	assert.Empty(t, d.Removed)
}

func TestComputeDiff_CommandQoSChangeCountsAsChanged(t *testing.T) {
	current := []sdk.SubscriptionSpec{{Topic: "actuators/a", Writable: true, CommandTopic: "actuators/a", CommandQoS: 1}}
	desired := []sdk.SubscriptionSpec{{Topic: "actuators/a", Writable: true, CommandTopic: "actuators/a", CommandQoS: 2}}
	d := computeDiff(current, desired)
	assert.Equal(t, []string{"actuators/a"}, topics(d.Changed))
}

func TestComputeDiff_IdenticalSetsProduceNoChanges(t *testing.T) {
	specs := []sdk.SubscriptionSpec{{Topic: "sensors/a", QoS: 1, DeviceRef: "dev-a"}}
	d := computeDiff(specs, specs)
	assert.Empty(t, d.Added)
	assert.Empty(t, d.Changed)
	assert.Empty(t, d.Removed)
}

func TestRevisionOf_StableAcrossOrderChangesInsensitiveToInputOrder(t *testing.T) {
	a := []sdk.SubscriptionSpec{{Topic: "sensors/a"}, {Topic: "sensors/b"}}
	b := []sdk.SubscriptionSpec{{Topic: "sensors/b"}, {Topic: "sensors/a"}}
	require.Equal(t, revisionOf(a), revisionOf(b))
}

func TestRevisionOf_ChangesWhenContentChanges(t *testing.T) {
	a := []sdk.SubscriptionSpec{{Topic: "sensors/a", QoS: 1}}
	b := []sdk.SubscriptionSpec{{Topic: "sensors/a", QoS: 2}}
	assert.NotEqual(t, revisionOf(a), revisionOf(b))
}
