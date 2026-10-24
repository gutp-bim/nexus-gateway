// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	pb "nexus-gateway/gen"
	"nexus-gateway/internal/common"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/storeforward"
)

// TestClockContractPipeline uses only injected BACnet responses, in-process
// normalization, temporary SQLite and the pinned offline RI oracle. No sockets.
// Set NEXUS_CLOCK_PYTHON explicitly in CI to require the Python bridge.
func TestClockContractPipeline(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	python := os.Getenv("NEXUS_CLOCK_PYTHON")
	if python == "" {
		python = filepath.Join(root, "connector/bacnet/.venv/bin/python")
		if _, err := os.Stat(python); os.IsNotExist(err) {
			t.Skip("BACnet test venv absent; set NEXUS_CLOCK_PYTHON to require cross-language clock contract tests")
		}
	}
	python, err = filepath.Abs(python)
	require.NoError(t, err)
	bridge := func(t *testing.T, mode string, payload any) []byte {
		t.Helper()
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		cmd := exec.CommandContext(t.Context(), python, filepath.Join(root, "connector/bacnet/tests/clock_contract_support.py"), mode)
		cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(root, "connector/bacnet"), "PYTHONDONTWRITEBYTECODE=1")
		cmd.Stdin = bytes.NewReader(data)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		result, err := cmd.Output()
		require.NoError(t, err, "%s: %s", mode, stderr.String())
		return result
	}

	for _, fixture := range []clockPipelineFixture{syntheticClockPipelineFixture(t), builderClockPipelineFixture(t)} {
		t.Run(fixture.Name, func(t *testing.T) {
			runClockContractPipeline(t, func(mode string, payload any) []byte { return bridge(t, mode, payload) }, fixture)
		})
	}
}

type clockPipelineFixture struct {
	Name         string
	Snapshot     []byte
	GatewayID    string
	ConnectorID  string
	DeviceID     int
	ClockLocalID string
	Points       []pointlist.Entry
}

func syntheticClockPipelineFixture(t *testing.T) clockPipelineFixture {
	data := clockFixture(t)
	var s clockSnapshot
	require.NoError(t, json.Unmarshal(data, &s))
	points := make([]pointlist.Entry, len(s.Points))
	for i, p := range s.Points {
		points[i] = pointlist.Entry{PointID: p.PointID, ConnectorID: "bacnet-example", Protocol: p.Protocol,
			LocalID: p.Native.ObjectType + "," + p.Native.InstanceNo, DeviceRef: p.Native.DeviceID, Unit: p.Unit, Writable: *p.Writable}
	}
	return clockPipelineFixture{Name: "synthetic", Snapshot: data, GatewayID: s.GatewayID, ConnectorID: "bacnet-example", DeviceID: 1234,
		ClockLocalID: "analogValue,2", Points: points}
}

func runClockContractPipeline(t *testing.T, bridge func(string, any) []byte, fixture clockPipelineFixture) {
	cfg, entries, err := StageClockSnapshot(fixture.Snapshot, fixture.GatewayID, fixture.ConnectorID, fixture.DeviceID)
	require.NoError(t, err)
	require.Len(t, cfg.Points, 35)
	require.Len(t, cfg.Clocks, 1)
	require.Len(t, entries, len(fixture.Points))
	for i, e := range entries {
		// The CSV expectation covers addressing, units and writeability; control
		// schemas are outside this fixture's projected columns.
		e.ControlSchema = ""
		require.Equal(t, fixture.Points[i], e, "preserve CSV/native/unit/writability mapping")
	}
	require.Equal(t, fixture.ClockLocalID, cfg.Clocks[0].LocalID)
	native, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(native), "point_id")
	require.NotContains(t, string(native), "BPT-")
	byLocalID := map[string]pointlist.Entry{}
	values := map[string]float64{}
	for i, p := range fixture.Points {
		require.Equal(t, NativeClockPoint{LocalID: p.LocalID, DeviceRef: p.DeviceRef, Unit: p.Unit, Writable: p.Writable}, cfg.Points[i])
		byLocalID[p.LocalID] = p
		values[p.LocalID] = float64(i) + 0.5
	}
	require.NotContains(t, byLocalID, cfg.Clocks[0].LocalID, "clock must not be an equipment/control target")
	var generated []struct {
		Name            string            `json:"name"`
		ExpectedQuality string            `json:"expectedQuality"`
		Events          []json.RawMessage `json:"events"`
		Acquisition     json.RawMessage   `json:"acquisition"`
	}
	require.NoError(t, json.Unmarshal(bridge("generate", cfg), &generated))
	require.Len(t, generated, 15)
	resolver := pointlist.NewSynced(nil)
	resolver.Update(entries)
	type recordedPoll struct {
		Name            string            `json:"name"`
		ExpectedQuality string            `json:"expectedQuality"`
		Acquisition     json.RawMessage   `json:"acquisition"`
		Frames          []json.RawMessage `json:"frames"`
	}
	polls := make([]recordedPoll, len(generated))
	var expected []*pb.TelemetryFrame
	path := filepath.Join(t.TempDir(), "clock-contract.db")
	buffer, err := storeforward.Open(path, 1024)
	require.NoError(t, err)
	t.Cleanup(func() { _ = buffer.Close() })
	for i, poll := range generated {
		polls[i] = recordedPoll{Name: poll.Name, ExpectedQuality: poll.ExpectedQuality, Acquisition: poll.Acquisition}
		seen := map[string]bool{}
		for _, event := range poll.Events {
			original := bytes.Clone(event)
			var evt common.Event
			require.NoError(t, json.Unmarshal(event, &evt))
			point, ok := byLocalID[evt.LocalID]
			require.True(t, ok, "unexpected native telemetry %s", evt.LocalID)
			require.False(t, seen[evt.LocalID], "duplicate telemetry %s", evt.LocalID)
			seen[evt.LocalID] = true
			if poll.Name == "partial equipment failure" && evt.LocalID == fixture.Points[1].LocalID {
				require.Equal(t, "Bad", evt.Quality, "retain equipment status independently of clock quality")
			} else {
				require.Equal(t, "Good", evt.Quality)
			}
			require.Equal(t, point.DeviceRef, evt.DeviceRef)
			require.Equal(t, point.Unit, evt.Unit)
			require.Equal(t, cfg.ConnectorID, evt.ConnectorID)
			frame, outcome := normalizer.Normalize(event, resolver, fixture.GatewayID)
			require.Equal(t, normalizer.OutcomeOK, outcome, poll.Name)
			require.Equal(t, point.PointID, frame.PointId, "resolve each native address in Normalizer")
			require.Equal(t, fixture.GatewayID, frame.GatewayId)
			require.Equal(t, values[evt.LocalID], frame.GetValueNum(), "preserve equipment values")
			if point.Unit == "" {
				require.NotContains(t, frame.Attributes, "unit", "empty units must be omitted downstream")
			} else {
				require.Equal(t, point.Unit, frame.Attributes["unit"], "preserve equipment units")
			}
			if evt.Quality == "Good" {
				require.NotContains(t, frame.Attributes, "quality")
			} else {
				require.Equal(t, evt.Quality, frame.Attributes["quality"])
			}
			require.Equal(t, evt.Timestamp, frame.Timestamp)
			require.Equal(t, cfg.Revision, frame.Attributes["sim_config_revision"])
			require.Equal(t, "absent", frame.Attributes["sim_run_status"])
			require.Equal(t, original, []byte(event), "normalization must not rewrite the event")
			require.Contains(t, frame.Timestamp, ".", "retain fractional acquisition UTC")
			require.NoError(t, buffer.Write(frame))
			expected = append(expected, proto.Clone(frame).(*pb.TelemetryFrame))
		}
		wantPoints := len(fixture.Points)
		if poll.Name == "partial equipment failure" {
			wantPoints--
		}
		require.Len(t, poll.Events, wantPoints)
		for i, p := range fixture.Points {
			require.Equal(t, !(poll.Name == "partial equipment failure" && i == 0), seen[p.LocalID], "poll coverage for %s", p.PointID)
		}
	}
	require.Len(t, expected, 524) // 15 polls x 35 points, minus one partial failure
	require.NoError(t, buffer.Close())
	// A changed current mapping must not re-resolve or relabel stored observations.
	resolver.Update([]pointlist.Entry{{ConnectorID: cfg.ConnectorID, LocalID: cfg.Points[0].LocalID, PointID: "future-point"}})
	buffer, err = storeforward.Open(path, 1024)
	require.NoError(t, err)
	for range 2 {
		batch, err := buffer.ReadBatch(0, 1024)
		require.NoError(t, err)
		require.Len(t, batch, len(expected))
		index := 0
		for i, poll := range generated {
			polls[i].Frames = nil
			for range poll.Events {
				frame := batch[index].Frame
				require.True(t, proto.Equal(expected[index], frame), "replay changed frame %d", index)
				data, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(frame)
				require.NoError(t, err)
				polls[i].Frames = append(polls[i].Frames, data)
				index++
			}
		}
		var result struct {
			VerifiedFrames int `json:"verifiedFrames"`
			VerifiedPolls  int `json:"verifiedPolls"`
		}
		require.NoError(t, json.Unmarshal(bridge("verify", map[string]any{"snapshot": json.RawMessage(fixture.Snapshot), "polls": polls}), &result))
		require.Equal(t, len(expected), result.VerifiedFrames)
		require.Equal(t, len(polls), result.VerifiedPolls)
	}
	for i, p := range fixture.Points {
		require.Equal(t, fmt.Sprintf("BPT-%04d", i+2), p.PointID)
	}
	t.Logf("%s: %d polls, %d real connector frames verified after two durable reads", fixture.Name, len(polls), len(expected))
}
