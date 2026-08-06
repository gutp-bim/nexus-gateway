package mqtt

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"nexus-gateway/internal/common"
)

type payloadOutcome uint8

const (
	payloadTelemetry payloadOutcome = iota
	payloadIgnored
	payloadInvalid
)

type decodedPayload struct {
	Value     common.Value
	Timestamp time.Time
}

type wirePayload struct {
	Datetime   string          `json:"datetime"`
	SnapshotAt string          `json:"snapshot_at"`
	Value      json.RawMessage `json:"value"`
	ValueUpper json.RawMessage `json:"Value"`
	V          json.RawMessage `json:"v"`
	Status     json.RawMessage `json:"status"`
}

// decodePayload converts the supported MQTT JSON envelope to the numeric
// Common Event contract. datetime is the source observation time;
// snapshot_at and finally the MQTT receive time are fallbacks.
func decodePayload(data []byte, receivedAt time.Time) (decodedPayload, payloadOutcome) {
	trimmed := bytes.TrimSpace(data)
	// Preserve the original connector contract for plain numeric payloads.
	if len(trimmed) > 0 && trimmed[0] != '{' {
		if value, ok := scalarValue(trimmed); ok {
			return decodedPayload{Value: value, Timestamp: receivedAt.UTC()}, payloadTelemetry
		}
		return decodedPayload{}, payloadInvalid
	}
	var p wirePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return decodedPayload{}, payloadInvalid
	}
	valueRaw := p.Value
	if len(valueRaw) == 0 {
		valueRaw = p.ValueUpper
	}
	if len(valueRaw) == 0 {
		valueRaw = p.V
	}
	if len(valueRaw) == 0 {
		if len(p.Status) != 0 {
			return decodedPayload{}, payloadIgnored
		}
		return decodedPayload{}, payloadInvalid
	}

	value, ok := scalarValue(valueRaw)
	if !ok {
		return decodedPayload{}, payloadInvalid
	}
	timestamp := receivedAt.UTC()
	for _, candidate := range []string{p.Datetime, p.SnapshotAt} {
		if candidate == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, candidate); err == nil {
			timestamp = parsed.UTC()
			break
		}
	}
	return decodedPayload{Value: value, Timestamp: timestamp}, payloadTelemetry
}

func scalarValue(raw json.RawMessage) (common.Value, bool) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return common.Value{}, false
	}
	if bytes.Equal(trimmed, []byte("true")) {
		return common.BoolValue(true), true
	}
	if bytes.Equal(trimmed, []byte("false")) {
		return common.BoolValue(false), true
	}
	if number, ok := numericValue(trimmed); ok {
		return common.NumberValue(number), true
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		if number, ok := numericValue(trimmed); ok {
			return common.NumberValue(number), true
		}
		return common.StringValue(text), true
	}
	return common.Value{}, false
}

func numericValue(raw json.RawMessage) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	var n json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&n); err == nil {
		if f, err := strconv.ParseFloat(n.String(), 64); err == nil && finite(f) {
			return f, true
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err == nil && finite(f) {
			return f, true
		}
	}
	return 0, false
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
