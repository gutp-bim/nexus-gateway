// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/pointlist"
)

// Independent expectations projected from the supplied Builder CSV, not from
// the generated snapshot or StageClockSnapshot's output.
func builderClockPipelineFixture(t *testing.T) clockPipelineFixture {
	t.Helper()
	root := "../../test/contracts/builder-ri-9083af2"
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		return data
	}
	var provenance struct {
		NexusBaselineSHA string            `json:"nexusBaselineSha"`
		RISourceSHA      string            `json:"riSourceSha"`
		BuilderSourceSHA string            `json:"builderSourceSha"`
		OracleSHA        string            `json:"oracleSha"`
		Files            map[string]string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(read("PROVENANCE.json"), &provenance))
	require.Equal(t, "6c7d3bf684373b13fc4ebfc5baac0e5f854fefdf", provenance.NexusBaselineSHA)
	require.Equal(t, "9083af2f851a38a442f90420e3cef913081c66a7", provenance.RISourceSHA)
	require.Equal(t, "f2f313e044b85f7ef127ef95351afef81afdcbc9", provenance.BuilderSourceSHA)
	require.Equal(t, "9b643391d7273de4d92166d2474de1e5e0ea9f83", provenance.OracleSHA)
	for name, digest := range provenance.Files {
		require.Equal(t, digest, fmt.Sprintf("%x", sha256.Sum256(read(name))), name)
	}
	data := read("provisioning.json")
	require.Equal(t, "b725e01075fbcfcf672e0a6f7021491d96ee584ce49de9bd8de12c0338d8e41e", fmt.Sprintf("%x", sha256.Sum256(data)), "RI output must remain byte-identical")
	var snapshot clockSnapshot
	require.NoError(t, json.Unmarshal(data, &snapshot))
	require.Len(t, snapshot.Points, 35)
	require.Len(t, snapshot.ClockSources, 1)
	require.Len(t, snapshot.ClockBindings, 1)
	require.Equal(t, "GW-BOPTEST-001", snapshot.GatewayID)
	clock := snapshot.ClockSources[0]
	require.Equal(t, "bacnet-boptest", clock.Native.ConnectorID)
	require.NotNil(t, clock.Native.DeviceInstance)
	require.Equal(t, 599, *clock.Native.DeviceInstance)
	require.NotNil(t, clock.Native.ObjectInstance)
	require.Equal(t, 2, *clock.Native.ObjectInstance)
	require.Equal(t, "analogValue", clock.Native.ObjectType)
	// The supplied sidecar has a Builder-only bindingId which is not part of
	// RI's native DTO; the shared clock fields must otherwise match exactly.
	var sidecar struct {
		ClockSources  []clockSource  `json:"clockSources"`
		ClockBindings []clockBinding `json:"clockBindings"`
	}
	require.NoError(t, json.Unmarshal(read("clocks.json"), &sidecar))
	require.Equal(t, snapshot.ClockSources, sidecar.ClockSources)
	require.Len(t, sidecar.ClockBindings, 1)
	rows, err := csv.NewReader(bytes.NewReader(read("points.csv"))).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 36)
	columns := map[string]int{}
	for i, key := range rows[0] {
		columns[key] = i
	}
	points := make([]pointlist.Entry, 0, 35)
	var raw struct {
		Points []map[string]json.RawMessage `json:"points"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	emptyUnits, writable := 0, 0
	for i, row := range rows[1:] {
		value := func(key string) string {
			index, ok := columns[key]
			require.True(t, ok, "missing CSV column %s", key)
			return row[index]
		}
		p := snapshot.Points[i]
		require.Equal(t, snapshot.GatewayID, value("gateway_id"))
		require.Equal(t, value("point_id"), p.PointID)
		require.Equal(t, value("protocol"), p.Protocol)
		require.Equal(t, value("protocol"), p.Native.Protocol)
		require.Equal(t, value("device_id_bacnet"), p.Native.DeviceID)
		require.Equal(t, value("object_type_bacnet"), p.Native.ObjectType)
		require.Equal(t, value("instance_no_bacnet"), p.Native.InstanceNo)
		localID := value("object_type_bacnet") + "," + value("instance_no_bacnet")
		require.Equal(t, localID, p.LocalID)
		write, err := strconv.ParseBool(value("writable"))
		require.NoError(t, err)
		require.NotNil(t, p.Writable)
		require.Equal(t, write, *p.Writable)
		if write {
			writable++
		}
		require.Equal(t, value("unit"), p.Unit)
		if p.Unit == "" {
			emptyUnits++
			require.NotContains(t, raw.Points[i], "unit", "RI omits empty units")
		}
		require.NotNil(t, p.Device)
		require.Equal(t, snapshot.ClockBindings[0].EquipmentID, p.Device.DtID)
		require.Equal(t, sidecar.ClockBindings[0].EquipmentID, p.Device.ID)
		require.Equal(t, clock.ClockID, snapshot.ClockBindings[0].ClockID)
		points = append(points, pointlist.Entry{PointID: value("point_id"), ConnectorID: clock.Native.ConnectorID, Protocol: value("protocol"), LocalID: localID, DeviceRef: value("device_id_bacnet"), Unit: value("unit"), Writable: write})
	}
	require.Equal(t, 4, emptyUnits)
	require.Equal(t, 4, writable)
	return clockPipelineFixture{Name: "builder-ri", Snapshot: data, GatewayID: snapshot.GatewayID, ConnectorID: clock.Native.ConnectorID,
		DeviceID: *clock.Native.DeviceInstance, ClockLocalID: fmt.Sprintf("%s,%d", clock.Native.ObjectType, *clock.Native.ObjectInstance), Points: points}
}

// The provenance/CSV check runs even when the optional Python venv is absent.
func TestBuilderClockSnapshotProvenance(t *testing.T) { _ = builderClockPipelineFixture(t) }
