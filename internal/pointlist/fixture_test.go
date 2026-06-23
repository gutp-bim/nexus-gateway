// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointlist_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus-gateway/internal/pointlist"
)

func TestFixture_ResolvesKnownEntry(t *testing.T) {
	f := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "dev/temp", PointID: "zone/temp"},
	})
	got, ok := f.Resolve("c1", "dev/temp")
	assert.True(t, ok)
	assert.Equal(t, "zone/temp", got)
}

func TestFixture_ReturnsFalseForUnknown(t *testing.T) {
	f := pointlist.NewFixture(nil)
	_, ok := f.Resolve("c1", "dev/temp")
	assert.False(t, ok)
}

func TestFixture_SameLocalIDDifferentConnectorAreDistinct(t *testing.T) {
	f := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c1", LocalID: "dev/temp", PointID: "zone1/temp"},
		{ConnectorID: "c2", LocalID: "dev/temp", PointID: "zone2/temp"},
	})
	p1, _ := f.Resolve("c1", "dev/temp")
	p2, _ := f.Resolve("c2", "dev/temp")
	assert.Equal(t, "zone1/temp", p1)
	assert.Equal(t, "zone2/temp", p2)
}
