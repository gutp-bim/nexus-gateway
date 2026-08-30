// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus-gateway/internal/metrics"
)

func TestProvisioningPromoted_DefaultsFalse(t *testing.T) {
	metrics.SetProvisioningPromoted(false)
	assert.False(t, metrics.ProvisioningPromoted())
	assert.Equal(t, 0, metrics.ProvisioningPromotedGauge())
}

func TestProvisioningPromoted_SetTrue(t *testing.T) {
	metrics.SetProvisioningPromoted(true)
	t.Cleanup(func() { metrics.SetProvisioningPromoted(false) })

	assert.True(t, metrics.ProvisioningPromoted())
	assert.Equal(t, 1, metrics.ProvisioningPromotedGauge())
}
