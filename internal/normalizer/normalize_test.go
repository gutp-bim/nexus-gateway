// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package normalizer_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/common"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
)

// These tests exercise normalizer.Normalize directly, with no NATS/JetStream.
// They are the fast unit-level complement to the JetStream integration tests.

func makeEvent(connID, localID string, value float64) []byte {
	evt := common.Event{
		ConnectorID: connID,
		LocalID:     localID,
		Value:       value,
		Timestamp:   "2025-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(evt)
	return data
}

func TestNormalize_HappyPath(t *testing.T) {
	resolver := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c1", LocalID: "l1", PointID: "p1"},
	})
	frame, out := normalizer.Normalize(makeEvent("c1", "l1", 42.5), resolver, "gw-x")
	assert.Equal(t, normalizer.OutcomeOK, out)
	require.NotNil(t, frame)
	assert.Equal(t, "gw-x", frame.GatewayId)
	assert.Equal(t, "p1", frame.PointId)
	assert.InDelta(t, 42.5, frame.Value, 0.001)
	assert.Equal(t, "2025-01-01T00:00:00Z", frame.Timestamp)
}

func TestNormalize_MissingTimestampFilled(t *testing.T) {
	resolver := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c1", LocalID: "l1", PointID: "p1"},
	})
	evt, _ := json.Marshal(common.Event{ConnectorID: "c1", LocalID: "l1", Value: 1.0})
	frame, out := normalizer.Normalize(evt, resolver, "gw-x")
	assert.Equal(t, normalizer.OutcomeOK, out)
	require.NotNil(t, frame)
	assert.NotEmpty(t, frame.Timestamp, "timestamp must be filled in when absent from event")
	// Verify it parses as RFC3339
	_, err := time.Parse(time.RFC3339, frame.Timestamp)
	assert.NoError(t, err)
}

func TestNormalize_UnknownLocalID_ReturnsMiss(t *testing.T) {
	resolver := pointlist.NewFixture(nil)
	frame, out := normalizer.Normalize(makeEvent("c1", "unknown", 1.0), resolver, "gw-x")
	assert.Equal(t, normalizer.OutcomeMiss, out)
	assert.Nil(t, frame)
}

func TestNormalize_InvalidJSON_ReturnsPoison(t *testing.T) {
	resolver := pointlist.NewFixture(nil)
	frame, out := normalizer.Normalize([]byte("{not valid"), resolver, "gw-x")
	assert.Equal(t, normalizer.OutcomePoison, out)
	assert.Nil(t, frame)
}

func TestNormalize_EmptyPayload_ReturnsPoison(t *testing.T) {
	resolver := pointlist.NewFixture(nil)
	frame, out := normalizer.Normalize([]byte{}, resolver, "gw-x")
	assert.Equal(t, normalizer.OutcomePoison, out)
	assert.Nil(t, frame)
}

func TestNormalize_LargeValue_Passthrough(t *testing.T) {
	resolver := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c1", LocalID: "l1", PointID: "p1"},
	})
	frame, out := normalizer.Normalize(makeEvent("c1", "l1", 1e15), resolver, "gw-x")
	assert.Equal(t, normalizer.OutcomeOK, out)
	require.NotNil(t, frame)
	assert.InDelta(t, 1e15, frame.Value, 1)
}

func TestNormalize_TimestampPreserved(t *testing.T) {
	// If event carries a timestamp, it must survive unchanged (no UTC normalization silently strips tz).
	resolver := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c1", LocalID: "l1", PointID: "p1"},
	})
	ts := "2024-06-01T12:00:00+09:00"
	data, _ := json.Marshal(common.Event{ConnectorID: "c1", LocalID: "l1", Value: 1.0, Timestamp: ts})
	frame, out := normalizer.Normalize(data, resolver, "gw-x")
	assert.Equal(t, normalizer.OutcomeOK, out)
	require.NotNil(t, frame)
	assert.True(t, strings.Contains(frame.Timestamp, "09:00") || frame.Timestamp == ts,
		"event timestamp must be passed through: got %q", frame.Timestamp)
}
