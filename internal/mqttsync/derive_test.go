// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqttsync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/pointlist"
)

func TestDerive_FiltersByProtocolAndConnector(t *testing.T) {
	entries := []pointlist.Entry{
		{ConnectorID: "mqtt-01", Protocol: "mqtt", LocalID: "sensors/a", PointID: "p-a"},
		{ConnectorID: "mqtt-02", Protocol: "mqtt", LocalID: "sensors/other-connector", PointID: "p-x"},
		{ConnectorID: "mqtt-01", Protocol: "bacnet", LocalID: "not-mqtt", PointID: "p-y"},
		{ConnectorID: "mqtt-01", Protocol: "mqtt", LocalID: "", PointID: "p-empty"}, // no topic — skipped
	}

	specs := Derive(entries, "mqtt-01", 1, 1)
	require.Len(t, specs, 1)
	assert.Equal(t, "sensors/a", specs[0].Topic)
}

func TestDerive_WritablePointGetsCommandTopicEqualToTopic(t *testing.T) {
	entries := []pointlist.Entry{
		{ConnectorID: "mqtt-01", Protocol: "mqtt", LocalID: "actuators/valve", Writable: true, DeviceRef: "dev-1", Unit: "bool"},
	}
	specs := Derive(entries, "mqtt-01", 1, 2)
	require.Len(t, specs, 1)
	spec := specs[0]
	assert.True(t, spec.Writable)
	assert.Equal(t, "actuators/valve", spec.Topic)
	assert.Equal(t, "actuators/valve", spec.CommandTopic, "writable point's command topic must equal its subscribe topic (confirmed convention, docs/adr/0008)")
	assert.EqualValues(t, 1, spec.QoS)
	assert.EqualValues(t, 2, spec.CommandQoS)
	assert.Equal(t, "dev-1", spec.DeviceRef)
	assert.Equal(t, "bool", spec.Unit)
}

func TestDerive_ReadOnlyPointHasNoCommandTopic(t *testing.T) {
	entries := []pointlist.Entry{
		{ConnectorID: "mqtt-01", Protocol: "mqtt", LocalID: "sensors/temp", Writable: false},
	}
	specs := Derive(entries, "mqtt-01", 1, 1)
	require.Len(t, specs, 1)
	assert.False(t, specs[0].Writable)
	assert.Empty(t, specs[0].CommandTopic)
	assert.Zero(t, specs[0].CommandQoS)
}

func TestDerive_AppliesGivenQoSDefaults(t *testing.T) {
	entries := []pointlist.Entry{
		{ConnectorID: "mqtt-01", Protocol: "mqtt", LocalID: "sensors/temp"},
	}
	specs := Derive(entries, "mqtt-01", 2, 0)
	require.Len(t, specs, 1)
	assert.EqualValues(t, 2, specs[0].QoS)
}
