// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/connector/sdk"
)

func gauge(t *testing.T, ms []sdk.Metric, name string) int64 {
	t.Helper()
	for _, m := range ms {
		if m.Name == name {
			return m.Value
		}
	}
	require.Failf(t, "metric not exposed", name)
	return 0
}

// A JetStream publish outage that outlasts publishStallThreshold turns /health
// unhealthy and shows up in mqtt_publish_stalled_seconds (#185); shorter blips and
// the recovered state stay healthy.
func TestHealthy_ReflectsContinuousPublishStall(t *testing.T) {
	c := &Connector{}
	c.connected.Store(true)

	assert.True(t, c.Healthy(), "no failures: healthy")
	assert.Zero(t, gauge(t, c.Metrics(), "mqtt_publish_stalled_seconds"))

	c.failingSince.Store(time.Now().Add(-publishStallThreshold / 2).UnixNano())
	assert.True(t, c.Healthy(), "a failure run shorter than the threshold is a transient blip")

	c.failingSince.Store(time.Now().Add(-2 * publishStallThreshold).UnixNano())
	assert.False(t, c.Healthy(), "failing past the threshold must report unhealthy")
	assert.GreaterOrEqual(t, gauge(t, c.Metrics(), "mqtt_publish_stalled_seconds"), int64(publishStallThreshold.Seconds()),
		"the gauge reports how long publishes have been failing")

	c.failingSince.Store(0) // a successful publish clears the run
	assert.True(t, c.Healthy())
	assert.Zero(t, gauge(t, c.Metrics(), "mqtt_publish_stalled_seconds"))
}

// Stalled or not, a lost broker session is still unhealthy.
func TestHealthy_RequiresBrokerConnection(t *testing.T) {
	c := &Connector{}
	assert.False(t, c.Healthy())
}
