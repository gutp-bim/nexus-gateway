// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// testdata/clock-provisioning-release.json is an unmodified RI clock snapshot of the
// BOPTEST testbed: 35 BACnet equipment points plus one RI #600 release point
// (BPT-0033-REL releases BPT-0033). Its revision and order are the RI's own.
func releaseClockFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/clock-provisioning-release.json")
	require.NoError(t, err)
	return b
}

func stageReleaseFixture(data []byte) (*NativeClockConfig, error) {
	cfg, _, err := StageClockSnapshot(data, "GW-BOPTEST-001", "bacnet-boptest", 599)
	return cfg, err
}

func TestStageClockAcceptsReleasePoint(t *testing.T) {
	cfg, entries, err := StageClockSnapshot(releaseClockFixture(t), "GW-BOPTEST-001", "bacnet-boptest", 599)
	require.NoError(t, err)
	require.Len(t, entries, 36)
	require.Len(t, cfg.Points, 35)
	require.Len(t, cfg.Clocks, 1)
	require.Len(t, cfg.Clocks[0].PointLocalIDs, 35)
	var release, target int
	for _, e := range entries {
		switch e.PointID {
		case "BPT-0033-REL":
			release++
			require.Equal(t, "BPT-0033", e.ReleaseOf)
			require.Equal(t, "bacnet", e.Protocol)
			require.Equal(t, "bacnet-boptest", e.ConnectorID)
			require.True(t, e.Writable)
			require.Empty(t, e.LocalID)
			require.JSONEq(t, `{"dataType":"enum","enumLabels":"{\"1\":\"release\"}"}`, e.ControlSchema)
		case "BPT-0033":
			target++
			require.Empty(t, e.ReleaseOf)
			require.Equal(t, "analogOutput,34", e.LocalID)
		default:
			require.Empty(t, e.ReleaseOf)
			require.NotEmpty(t, e.LocalID)
		}
	}
	require.Equal(t, 1, release)
	require.Equal(t, 1, target)
	// The release point is not a native object: nothing to poll, no clock membership.
	for _, p := range cfg.Points {
		require.NotEmpty(t, p.LocalID)
	}
	for _, id := range cfg.Clocks[0].PointLocalIDs {
		require.NotEmpty(t, id)
	}
	native, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(native), "BPT-")
	require.NotContains(t, string(native), "release")
}

func TestStageClockReleasePointWithoutDeviceIsNotEquipmentMembership(t *testing.T) {
	data := mutateClockData(t, releaseClockFixture(t), func(b map[string]any) { delete(releasePoint(t, b), "device") }, true)
	cfg, err := stageReleaseFixture(data)
	require.NoError(t, err)
	require.Len(t, cfg.Points, 35)
}

func releasePoint(t *testing.T, b map[string]any) map[string]any {
	t.Helper()
	for _, p := range b["points"].([]any) {
		if p.(map[string]any)["releaseOf"] != nil {
			return p.(map[string]any)
		}
	}
	t.Fatal("fixture has no release point")
	return nil
}

func pointByID(t *testing.T, b map[string]any, id string) map[string]any {
	t.Helper()
	for _, p := range b["points"].([]any) {
		if p.(map[string]any)["pointId"] == id {
			return p.(map[string]any)
		}
	}
	t.Fatalf("fixture has no point %s", id)
	return nil
}

// addReleasePoint appends a copy of the fixture's release point and keeps the
// canonical pointId order so that only the semantic rule under test fails.
func addReleasePoint(t *testing.T, b map[string]any, pointID, releaseOf string) {
	t.Helper()
	data, err := json.Marshal(releasePoint(t, b))
	require.NoError(t, err)
	var p map[string]any
	require.NoError(t, json.Unmarshal(data, &p))
	p["pointId"], p["releaseOf"] = pointID, releaseOf
	points := append(b["points"].([]any), p)
	sort.Slice(points, func(i, j int) bool {
		return points[i].(map[string]any)["pointId"].(string) < points[j].(map[string]any)["pointId"].(string)
	})
	b["points"] = points
}

func TestStageClockRejectsInvalidReleasePoints(t *testing.T) {
	tests := map[string]func(*testing.T, map[string]any){
		"target missing":           func(t *testing.T, b map[string]any) { releasePoint(t, b)["releaseOf"] = "BPT-9999" },
		"target not writable":      func(t *testing.T, b map[string]any) { pointByID(t, b, "BPT-0033")["writable"] = false },
		"target read-only point":   func(t *testing.T, b map[string]any) { releasePoint(t, b)["releaseOf"] = "BPT-0002" },
		"target is release point":  func(t *testing.T, b map[string]any) { addReleasePoint(t, b, "BPT-0033-REL2", "BPT-0033-REL") },
		"self release":             func(t *testing.T, b map[string]any) { releasePoint(t, b)["releaseOf"] = "BPT-0033-REL" },
		"non-bacnet protocol":      func(t *testing.T, b map[string]any) { releasePoint(t, b)["protocol"] = "mqtt" },
		"missing protocol":         func(t *testing.T, b map[string]any) { delete(releasePoint(t, b), "protocol") },
		"two releases for target":  func(t *testing.T, b map[string]any) { addReleasePoint(t, b, "BPT-0033-REL2", "BPT-0033") },
		"release not writable":     func(t *testing.T, b map[string]any) { releasePoint(t, b)["writable"] = false },
		"release writable missing": func(t *testing.T, b map[string]any) { delete(releasePoint(t, b), "writable") },
		"release with localId":     func(t *testing.T, b map[string]any) { releasePoint(t, b)["localId"] = "analogValue,99" },
		"release with native block": func(t *testing.T, b map[string]any) {
			releasePoint(t, b)["native"] = map[string]any{"protocol": "bacnet", "deviceId": "599", "objectType": "analogValue", "instanceNo": "99"}
		},
		"duplicate release pointId": func(t *testing.T, b map[string]any) {
			addReleasePoint(t, b, "BPT-0033-REL", "BPT-0034")
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			data := mutateClockData(t, releaseClockFixture(t), func(b map[string]any) { change(t, b) }, true)
			cfg, entries, err := StageClockSnapshot(data, "GW-BOPTEST-001", "bacnet-boptest", 599)
			require.Error(t, err)
			require.Nil(t, cfg)
			require.Nil(t, entries)
		})
	}
}
