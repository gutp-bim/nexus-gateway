// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	pb "nexus-gateway/gen"
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
	bridge := func(mode string, payload any) []byte {
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

	cfg, entries, err := StageClockSnapshot(clockFixture(t), "GW-EXAMPLE", "bacnet-example", 1234)
	require.NoError(t, err)
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
		for _, event := range poll.Events {
			original := bytes.Clone(event)
			frame, outcome := normalizer.Normalize(event, resolver, "GW-EXAMPLE")
			require.Equal(t, normalizer.OutcomeOK, outcome, poll.Name)
			require.Equal(t, 0.5, frame.GetValueNum(), "preserve equipment values")
			require.Equal(t, "1", frame.Attributes["unit"], "preserve equipment units")
			require.Equal(t, original, []byte(event), "normalization must not rewrite the event")
			require.Contains(t, frame.Timestamp, ".", "retain fractional acquisition UTC")
			require.NoError(t, buffer.Write(frame))
			expected = append(expected, proto.Clone(frame).(*pb.TelemetryFrame))
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
		require.NoError(t, json.Unmarshal(bridge("verify", polls), &result))
		require.Equal(t, len(expected), result.VerifiedFrames)
		require.Equal(t, len(polls), result.VerifiedPolls)
	}
	t.Logf("RI ADR-0010: %d polls, %d real connector frames verified after two durable reads", len(polls), len(expected))
}
