// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus-gateway/internal/storeforward"
)

// TestApplyAck_AllAccepted: server acks every frame — no drift, cursor at last seq.
func TestApplyAck_AllAccepted(t *testing.T) {
	batch := []storeforward.SentFrame{
		{Seq: 10, PointID: "p1"},
		{Seq: 11, PointID: "p2"},
		{Seq: 12, PointID: "p3"},
	}
	cursor, drifts := storeforward.ApplyAck(batch, 3)
	assert.Equal(t, int64(12), cursor)
	assert.Empty(t, drifts)
}

// TestApplyAck_PartialAccepted: server accepts 2 of 3 — third gets drift.
func TestApplyAck_PartialAccepted(t *testing.T) {
	batch := []storeforward.SentFrame{
		{Seq: 10, PointID: "p1"},
		{Seq: 11, PointID: "p2"},
		{Seq: 12, PointID: "p3"},
	}
	cursor, drifts := storeforward.ApplyAck(batch, 2)
	assert.Equal(t, int64(12), cursor, "cursor must advance to last seq regardless")
	assert.Equal(t, int64(1), drifts["p3"], "only the rejected frame accrues drift")
	assert.Zero(t, drifts["p1"])
	assert.Zero(t, drifts["p2"])
}

// TestApplyAck_NoneAccepted: server accepts zero — all frames drift.
func TestApplyAck_NoneAccepted(t *testing.T) {
	batch := []storeforward.SentFrame{
		{Seq: 1, PointID: "p1"},
		{Seq: 2, PointID: "p1"},
		{Seq: 3, PointID: "p2"},
	}
	cursor, drifts := storeforward.ApplyAck(batch, 0)
	assert.Equal(t, int64(3), cursor)
	assert.Equal(t, int64(2), drifts["p1"])
	assert.Equal(t, int64(1), drifts["p2"])
}

// TestApplyAck_NegativeAccepted: guard against buggy server returning negative.
// Must treat as 0 (all frames drift).
func TestApplyAck_NegativeAccepted(t *testing.T) {
	batch := []storeforward.SentFrame{
		{Seq: 5, PointID: "p1"},
		{Seq: 6, PointID: "p2"},
	}
	cursor, drifts := storeforward.ApplyAck(batch, -1)
	assert.Equal(t, int64(6), cursor)
	assert.Equal(t, int64(1), drifts["p1"])
	assert.Equal(t, int64(1), drifts["p2"])
}

// TestApplyAck_EmptyBatch: no frames sent — cursor stays zero, no drifts.
func TestApplyAck_EmptyBatch(t *testing.T) {
	cursor, drifts := storeforward.ApplyAck(nil, 0)
	assert.Zero(t, cursor)
	assert.Empty(t, drifts)
}

// TestApplyAck_MultiPointDrift: multiple frames from the same point each count separately.
func TestApplyAck_MultiPointDrift(t *testing.T) {
	batch := []storeforward.SentFrame{
		{Seq: 1, PointID: "temp"},
		{Seq: 2, PointID: "temp"},
		{Seq: 3, PointID: "temp"},
		{Seq: 4, PointID: "hum"},
	}
	cursor, drifts := storeforward.ApplyAck(batch, 1) // only first accepted
	assert.Equal(t, int64(4), cursor)
	assert.Equal(t, int64(2), drifts["temp"])
	assert.Equal(t, int64(1), drifts["hum"])
}
