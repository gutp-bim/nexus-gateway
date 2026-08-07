package mqtt

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/common"
)

func TestDecodePayload_ObservedAWSShapes(t *testing.T) {
	received := time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		payload   string
		value     float64
		timestamp string
	}{
		{"mist number", `{"datetime":"2026-08-06T12:44:33Z","snapshot_at":"2026-08-06T12:44:33Z","value":0}`, 0, "2026-08-06T12:44:33Z"},
		{"HVAC numeric string", `{"datetime":"2026-08-06T12:50:37Z","value":"0.0"}`, 0, "2026-08-06T12:50:37Z"},
		{"light numeric string", `{"datetime":"2026-08-06T12:50:37Z","value":"0"}`, 0, "2026-08-06T12:50:37Z"},
		{"energy numeric string", `{"datetime":"2026-08-06T12:51:37Z","value":"311148"}`, 311148, "2026-08-06T12:51:37Z"},
		{"decimal number", `{"datetime":"2026-08-06T12:51:37Z","value":12.5}`, 12.5, "2026-08-06T12:51:37Z"},
		{"snapshot fallback", `{"datetime":"bad","snapshot_at":"2026-08-06T12:44:33Z","value":1}`, 1, "2026-08-06T12:44:33Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome := decodePayload([]byte(tc.payload), received)
			require.Equal(t, payloadTelemetry, outcome)
			value, ok := got.Value.Number()
			require.True(t, ok)
			assert.Equal(t, tc.value, value)
			assert.Equal(t, tc.timestamp, got.Timestamp.Format(time.RFC3339))
		})
	}
}

func TestDecodePayload_HeartbeatIgnored(t *testing.T) {
	_, outcome := decodePayload([]byte(`{"datetime":"2026-08-06T12:48:03Z","status":"healthy"}`), time.Now())
	assert.Equal(t, payloadIgnored, outcome)
}

func TestDecodePayload_InvalidValues(t *testing.T) {
	for _, payload := range []string{
		`{"datetime":"2026-08-06T12:48:03Z","value":null}`,
		`not-json`,
	} {
		_, outcome := decodePayload([]byte(payload), time.Now())
		assert.Equal(t, payloadInvalid, outcome, payload)
	}
}

func TestDecodePayload_GeneralStringPreserved(t *testing.T) {
	for _, value := range []string{"running", "運転", ""} {
		payload, err := json.Marshal(map[string]any{
			"datetime": "2026-08-06T12:48:03Z",
			"value":    value,
		})
		require.NoError(t, err)
		got, outcome := decodePayload(payload, time.Now())
		require.Equal(t, payloadTelemetry, outcome)
		assert.Equal(t, common.ValueString, got.Value.Kind())
		gotValue, ok := got.Value.String()
		require.True(t, ok)
		assert.Equal(t, value, gotValue)
	}
}

func TestDecodePayload_BooleanPreserved(t *testing.T) {
	for _, value := range []bool{true, false} {
		payload, err := json.Marshal(map[string]any{"value": value})
		require.NoError(t, err)
		got, outcome := decodePayload(payload, time.Now())
		require.Equal(t, payloadTelemetry, outcome)
		assert.Equal(t, common.ValueBool, got.Value.Kind())
		gotValue, ok := got.Value.Bool()
		require.True(t, ok)
		assert.Equal(t, value, gotValue)
	}
}
