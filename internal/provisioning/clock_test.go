// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/storeforward"
)

func clockFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/clock-provisioning.json")
	require.NoError(t, err)
	return b
}

// Re-sign mutations to exercise semantic validation separately from integrity.
func mutateClockFixture(t *testing.T, change func(map[string]any), resign bool) []byte {
	t.Helper()
	var b map[string]any
	require.NoError(t, json.Unmarshal(clockFixture(t), &b))
	change(b)
	if resign {
		delete(b, "revision")
		delete(b, "generatedAt")
		data, err := json.Marshal(b)
		require.NoError(t, err)
		b["revision"] = fmt.Sprintf("\"sha256:%x\"", sha256.Sum256(data))
	}
	data, err := json.Marshal(b)
	require.NoError(t, err)
	return data
}

func TestStageClockFixture(t *testing.T) {
	cfg, entries, err := StageClockSnapshot(clockFixture(t), "GW-EXAMPLE", "bacnet-example", 1234)
	require.NoError(t, err)
	require.Len(t, cfg.Points, 35)
	require.Len(t, entries, 35)
	require.Len(t, cfg.Clocks, 1)
	require.Len(t, cfg.Clocks[0].PointLocalIDs, 35)
	require.Equal(t, "analogValue,2", cfg.Clocks[0].LocalID)
	for i, e := range entries {
		require.Equal(t, fmt.Sprintf("BPT-%04d", i+2), e.PointID)
	}
	native, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(native), "BPT-")
	require.NotContains(t, string(native), "equipment-")
}

func TestStageClockRejectsInvalidSnapshots(t *testing.T) {
	clock := func(b map[string]any) map[string]any { return b["clockSources"].([]any)[0].(map[string]any) }
	native := func(b map[string]any) map[string]any { return clock(b)["native"].(map[string]any) }
	point := func(b map[string]any) map[string]any { return b["points"].([]any)[0].(map[string]any) }
	tests := map[string]func(map[string]any){
		"schema":             func(b map[string]any) { b["schemaVersion"] = "future" },
		"gateway":            func(b map[string]any) { b["gatewayId"] = "other" },
		"missing array":      func(b map[string]any) { delete(b, "clockBindings") },
		"unit":               func(b map[string]any) { clock(b)["unit"] = "min" },
		"writable clock":     func(b map[string]any) { clock(b)["readOnly"] = false },
		"cov":                func(b map[string]any) { clock(b)["acquisitionMode"] = "cov" },
		"native scope":       func(b map[string]any) { native(b)["connectorId"] = "other" },
		"native gateway":     func(b map[string]any) { native(b)["gatewayId"] = "other" },
		"native device":      func(b map[string]any) { native(b)["deviceInstance"] = 2 },
		"native unit":        func(b map[string]any) { native(b)["unit"] = "minutes" },
		"missing instance":   func(b map[string]any) { delete(native(b), "objectInstance") },
		"negative instance":  func(b map[string]any) { native(b)["objectInstance"] = -1 },
		"wrong type":         func(b map[string]any) { native(b)["objectType"] = "binaryValue" },
		"wrong property":     func(b map[string]any) { native(b)["property"] = "objectName" },
		"static run":         func(b map[string]any) { clock(b)["runId"] = "forbidden" },
		"dangling binding":   func(b map[string]any) { b["clockBindings"].([]any)[0].(map[string]any)["clockId"] = "urn:missing" },
		"dangling equipment": func(b map[string]any) { b["clockBindings"].([]any)[0].(map[string]any)["equipmentId"] = "urn:missing" },
		"duplicate binding": func(b map[string]any) {
			b["clockBindings"] = append(b["clockBindings"].([]any), b["clockBindings"].([]any)[0])
		},
		"duplicate clock": func(b map[string]any) { b["clockSources"] = append(b["clockSources"].([]any), clock(b)) },
		"duplicate point": func(b map[string]any) { b["points"].([]any)[1].(map[string]any)["pointId"] = point(b)["pointId"] },
		"clock as point": func(b map[string]any) {
			n := point(b)["native"].(map[string]any)
			n["objectType"] = "analogValue"
			n["instanceNo"] = "2"
		},
		"different point revision": func(b map[string]any) { point(b)["native"].(map[string]any)["deviceId"] = "5678" },
		"missing equipment":        func(b map[string]any) { delete(point(b), "device") },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			cfg, entries, err := StageClockSnapshot(mutateClockFixture(t, change, true), "GW-EXAMPLE", "bacnet-example", 1234)
			require.Error(t, err)
			require.Nil(t, cfg)
			require.Nil(t, entries)
		})
	}
	_, _, err := StageClockSnapshot(mutateClockFixture(t, func(b map[string]any) { native(b)["objectInstance"] = 87 }, false), "GW-EXAMPLE", "bacnet-example", 1234)
	require.ErrorContains(t, err, "revision mismatch")
}

func TestStageClockAddressIsConfiguredAndEmptyClockSnapshot(t *testing.T) {
	b := mutateClockFixture(t, func(b map[string]any) {
		b["clockSources"].([]any)[0].(map[string]any)["native"].(map[string]any)["objectInstance"] = 87
	}, true)
	cfg, _, err := StageClockSnapshot(b, "GW-EXAMPLE", "bacnet-example", 1234)
	require.NoError(t, err)
	require.Equal(t, "analogValue,87", cfg.Clocks[0].LocalID)
	b = mutateClockFixture(t, func(b map[string]any) { b["clockSources"] = []any{}; b["clockBindings"] = []any{} }, true)
	cfg, entries, err := StageClockSnapshot(b, "GW-EXAMPLE", "bacnet-example", 1234)
	require.NoError(t, err)
	require.Empty(t, cfg.Clocks)
	require.Len(t, entries, 35)
}

func TestStageIndependentClockBindings(t *testing.T) {
	b := mutateClockFixture(t, func(b map[string]any) {
		clocks := b["clockSources"].([]any)
		data, err := json.Marshal(clocks[0])
		require.NoError(t, err)
		var second map[string]any
		require.NoError(t, json.Unmarshal(data, &second))
		second["clockId"] = "https://example.com/ri-606/clock/z-independent"
		second["native"].(map[string]any)["objectInstance"] = 87
		b["clockSources"] = append(clocks, second)
		b["clockBindings"].([]any)[1].(map[string]any)["clockId"] = second["clockId"]
	}, true)
	cfg, _, err := StageClockSnapshot(b, "GW-EXAMPLE", "bacnet-example", 1234)
	require.NoError(t, err)
	require.Len(t, cfg.Clocks, 2)
	require.Len(t, cfg.Clocks[0].PointLocalIDs, 18)
	require.Len(t, cfg.Clocks[1].PointLocalIDs, 17)
}

func TestStageRejectsAmbiguousJSON(t *testing.T) {
	fixture := string(clockFixture(t))
	for _, data := range []string{
		fixture + `{}`,
		strings.Replace(fixture, `"gatewayId": "GW-EXAMPLE"`, `"gatewayId":"wrong","gatewayId":"GW-EXAMPLE"`, 1),
		strings.Replace(fixture, `"generatedAt": "2026-01-01T00:00:00Z"`, `"generatedAt":null`, 1),
	} {
		cfg, entries, err := StageClockSnapshot([]byte(data), "GW-EXAMPLE", "bacnet-example", 1234)
		require.Error(t, err)
		require.Nil(t, cfg)
		require.Nil(t, entries)
	}
}

func TestClockAttributesSurviveNormalizerAndDurableReplay(t *testing.T) {
	cfg, entries, err := StageClockSnapshot(clockFixture(t), "GW-EXAMPLE", "bacnet-example", 1234)
	require.NoError(t, err)
	attrs := map[string]string{"sim_clock_id": cfg.Clocks[0].ClockID, "sim_clock_revision": cfg.Revision, "sim_time_s": "120.0", "sim_time_quality": "stable_unfenced"}
	event := map[string]any{"protocol": "bacnet", "connector_id": cfg.ConnectorID, "local_id": cfg.Points[0].LocalID, "device_ref": "1234", "value": 21.5, "quality": "Good", "timestamp": "2026-01-01T00:00:00Z", "attributes": attrs}
	data, err := json.Marshal(event)
	require.NoError(t, err)
	frame, outcome := normalizer.Normalize(data, pointlist.NewFixture(entries), "GW-EXAMPLE")
	require.Equal(t, normalizer.OutcomeOK, outcome)
	require.Equal(t, "BPT-0002", frame.PointId)
	require.Equal(t, "2026-01-01T00:00:00Z", frame.Timestamp)
	require.Equal(t, attrs, frame.Attributes)
	path := t.TempDir() + "/clock.db"
	buffer, err := storeforward.Open(path, 10)
	require.NoError(t, err)
	require.NoError(t, buffer.Write(frame))
	require.NoError(t, buffer.Close())
	buffer, err = storeforward.Open(path, 10)
	require.NoError(t, err)
	defer buffer.Close()
	for range 2 {
		batch, err := buffer.ReadBatch(0, 10)
		require.NoError(t, err)
		require.Len(t, batch, 1)
		require.Equal(t, attrs, batch[0].Frame.Attributes)
		require.Equal(t, frame.Timestamp, batch[0].Frame.Timestamp)
	}
	// Clock remains absent from the canonical resolver.
	_, found := pointlist.NewFixture(entries).Resolve(cfg.ConnectorID, cfg.Clocks[0].LocalID)
	require.False(t, found)
	require.False(t, strings.Contains(string(data), "sim_run_id"))
}
