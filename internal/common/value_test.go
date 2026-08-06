package common

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValue_JSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		kind ValueKind
		want any
	}{
		{"zero", `0`, ValueNumber, float64(0)},
		{"empty string", `""`, ValueString, ""},
		{"false", `false`, ValueBool, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var value Value
			require.NoError(t, json.Unmarshal([]byte(tc.in), &value))
			assert.Equal(t, tc.kind, value.Kind())
			assert.Equal(t, tc.want, value.Any())
			got, err := json.Marshal(value)
			require.NoError(t, err)
			assert.JSONEq(t, tc.in, string(got))
		})
	}
}

func TestValue_RejectsNonScalarAndNonFinite(t *testing.T) {
	for _, input := range []string{`null`, `{}`, `[]`, `1e9999`, `1 2`} {
		var value Value
		assert.Error(t, json.Unmarshal([]byte(input), &value), input)
	}
}
