// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

// ValueKind identifies the single JSON scalar carried by a Common Event.
type ValueKind uint8

const (
	ValueInvalid ValueKind = iota
	ValueNumber
	ValueString
	ValueBool
)

// Value is a validated discriminated telemetry scalar. Its zero value is invalid.
type Value struct {
	kind   ValueKind
	number float64
	text   string
	flag   bool
}

func NumberValue(value float64) Value { return Value{kind: ValueNumber, number: value} }
func StringValue(value string) Value  { return Value{kind: ValueString, text: value} }
func BoolValue(value bool) Value      { return Value{kind: ValueBool, flag: value} }

func (v Value) Kind() ValueKind { return v.kind }

func (v Value) Any() any {
	switch v.kind {
	case ValueNumber:
		return v.number
	case ValueString:
		return v.text
	case ValueBool:
		return v.flag
	default:
		return nil
	}
}

func (v Value) Number() (float64, bool) { return v.number, v.kind == ValueNumber }
func (v Value) String() (string, bool)  { return v.text, v.kind == ValueString }
func (v Value) Bool() (bool, bool)      { return v.flag, v.kind == ValueBool }

func (v Value) MarshalJSON() ([]byte, error) {
	if v.kind == ValueNumber && (math.IsNaN(v.number) || math.IsInf(v.number, 0)) {
		return nil, errors.New("telemetry number must be finite")
	}
	if v.kind == ValueInvalid {
		return nil, errors.New("telemetry value is missing")
	}
	return json.Marshal(v.Any())
}

func (v *Value) UnmarshalJSON(data []byte) error {
	*v = Value{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("decode telemetry value: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("telemetry value must contain one JSON scalar")
	}
	switch value := raw.(type) {
	case json.Number:
		number, err := value.Float64()
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return errors.New("telemetry number must be finite")
		}
		*v = NumberValue(number)
	case string:
		*v = StringValue(value)
	case bool:
		*v = BoolValue(value)
	default:
		return errors.New("telemetry value must be a number, string, or boolean")
	}
	return nil
}
