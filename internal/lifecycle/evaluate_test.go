// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"nexus-gateway/internal/lifecycle"
)

func healthyInputs() lifecycle.HealthInputs {
	return lifecycle.HealthInputs{
		NatsConnected:      true,
		UplinkConnected:    true,
		HasBuffer:          true,
		BufferDepth:        0,
		BufferCapacity:     1000,
		WriteErrors:        0,
		LastCheckpointUnix: time.Now().Unix(),
		HasPointList:       true,
		PointCount:         5,
		Connectors:         []lifecycle.ConnectorHealth{{ID: "c1", Running: true}},
		Now:                time.Now(),
	}
}

func componentByName(r lifecycle.HealthReport, name string) (lifecycle.ComponentHealth, bool) {
	for _, c := range r.Components {
		if c.Name == name {
			return c, true
		}
	}
	return lifecycle.ComponentHealth{}, false
}

// A fully healthy, quiet gateway evaluates to ok with every component ok.
func TestEvaluate_QuietHealthyIsOK(t *testing.T) {
	r := lifecycle.Evaluate(healthyInputs(), lifecycle.DefaultThresholds)
	assert.Equal(t, lifecycle.StatusOK, r.Status)
	for _, c := range r.Components {
		assert.Equal(t, lifecycle.StatusOK, c.Status, "component %s", c.Name)
	}
}

// Each degradation rule, in isolation, flips overall status to degraded and marks
// exactly its component degraded with a non-empty reason.
func TestEvaluate_DegradationRules(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*lifecycle.HealthInputs)
		component string
	}{
		{"nats down", func(in *lifecycle.HealthInputs) { in.NatsConnected = false }, "nats"},
		{"checkpoint stale with pending frames", func(in *lifecycle.HealthInputs) {
			in.BufferDepth = 10
			in.LastCheckpointUnix = time.Now().Add(-5 * time.Minute).Unix()
		}, "uplink"},
		{"write errors", func(in *lifecycle.HealthInputs) { in.WriteErrors = 3 }, "buffer"},
		{"near capacity", func(in *lifecycle.HealthInputs) { in.BufferDepth = 950; in.BufferCapacity = 1000 }, "buffer"},
		{"empty point list", func(in *lifecycle.HealthInputs) { in.PointCount = 0 }, "pointlist"},
		{"connector down", func(in *lifecycle.HealthInputs) {
			in.Connectors = []lifecycle.ConnectorHealth{{ID: "c1", Running: false}}
		}, "connectors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := healthyInputs()
			tc.mutate(&in)
			r := lifecycle.Evaluate(in, lifecycle.DefaultThresholds)

			assert.Equal(t, lifecycle.StatusDegraded, r.Status)
			c, ok := componentByName(r, tc.component)
			assert.True(t, ok, "component %s present", tc.component)
			assert.Equal(t, lifecycle.StatusDegraded, c.Status)
			assert.NotEmpty(t, c.Reason)
		})
	}
}

// A quiet gateway with an EMPTY backlog and an old checkpoint clock must NOT flap
// to degraded — staleness accrues only while frames are pending (#45 no-flap).
func TestEvaluate_QuietGatewayNoFlap(t *testing.T) {
	in := healthyInputs()
	in.BufferDepth = 0
	in.LastCheckpointUnix = time.Now().Add(-time.Hour).Unix() // old, but nothing pending
	r := lifecycle.Evaluate(in, lifecycle.DefaultThresholds)
	assert.Equal(t, lifecycle.StatusOK, r.Status)
	c, _ := componentByName(r, "uplink")
	assert.Equal(t, lifecycle.StatusOK, c.Status)
}

// When the buffer / point list sources are not configured, those components are
// omitted rather than reported as failures (a minimal server stays ok).
func TestEvaluate_OmitsUnconfiguredComponents(t *testing.T) {
	in := healthyInputs()
	in.HasBuffer = false
	in.HasPointList = false
	r := lifecycle.Evaluate(in, lifecycle.DefaultThresholds)
	assert.Equal(t, lifecycle.StatusOK, r.Status)
	_, hasBuffer := componentByName(r, "buffer")
	_, hasPL := componentByName(r, "pointlist")
	assert.False(t, hasBuffer)
	assert.False(t, hasPL)
}

// No connectors registered is not a failure (an empty set is ok, not degraded).
func TestEvaluate_NoConnectorsIsOK(t *testing.T) {
	in := healthyInputs()
	in.Connectors = nil
	r := lifecycle.Evaluate(in, lifecycle.DefaultThresholds)
	assert.Equal(t, lifecycle.StatusOK, r.Status)
}
