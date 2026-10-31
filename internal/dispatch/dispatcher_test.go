// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package dispatch_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/dispatch"
	"nexus-gateway/internal/pointlist"
)

func TestDispatch_HappyPath(t *testing.T) {
	nc := startNATS(t)
	resolver := buildResolver([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "p1",
			Writable: true, DeviceRef: "dev:1"},
	})
	d := dispatch.New(nc, resolver, 2*time.Second)

	// Register a fake connector that replies with success
	sub, err := nc.Subscribe("cmd.sim.sim-01", func(msg *nats.Msg) {
		reply := dispatch.ConnectorReply{Success: true, Response: "ok"}
		data, _ := json.Marshal(reply)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { sub.Unsubscribe() })

	result := d.Execute(context.Background(), &pb.ControlCommand{
		ControlId: "ctrl-1", PointId: "p1", PresentValue: 22.5, Priority: 8,
	})
	assert.True(t, result.Success)
	assert.Equal(t, "ctrl-1", result.ControlId)
}

func TestDispatch_NotWritable(t *testing.T) {
	nc := startNATS(t)
	resolver := buildResolver([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "p1",
			Writable: false, DeviceRef: "dev:1"},
	})
	d := dispatch.New(nc, resolver, 2*time.Second)

	// Connector subscription should NOT be called
	called := false
	sub, _ := nc.Subscribe("cmd.sim.sim-01", func(_ *nats.Msg) { called = true })
	t.Cleanup(func() { sub.Unsubscribe() })

	result := d.Execute(context.Background(), &pb.ControlCommand{
		ControlId: "ctrl-2", PointId: "p1", PresentValue: 1.0,
	})
	assert.False(t, result.Success)
	assert.Equal(t, "not_writable", result.Response)
	assert.False(t, called, "connector must not be called for read-only point")
}

func TestDispatch_NoConnector(t *testing.T) {
	nc := startNATS(t)
	resolver := buildResolver([]pointlist.Entry{}) // empty — unknown point_id
	d := dispatch.New(nc, resolver, 2*time.Second)

	result := d.Execute(context.Background(), &pb.ControlCommand{
		ControlId: "ctrl-3", PointId: "unknown-point",
	})
	assert.False(t, result.Success)
	assert.Equal(t, "no_connector", result.Response)
}

func TestDispatch_Timeout(t *testing.T) {
	nc := startNATS(t)
	resolver := buildResolver([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "p1",
			Writable: true, DeviceRef: "dev:1"},
	})
	// No subscriber → ErrNoResponders (connector process not running)
	d := dispatch.New(nc, resolver, 50*time.Millisecond)

	start := time.Now()
	result := d.Execute(context.Background(), &pb.ControlCommand{
		ControlId: "ctrl-4", PointId: "p1", PresentValue: 1.0,
	})
	assert.WithinDuration(t, start.Add(200*time.Millisecond), time.Now(), 300*time.Millisecond)
	assert.False(t, result.Success)
	assert.Equal(t, "no_responder", result.Response)
}

func TestDispatch_DeviceError(t *testing.T) {
	nc := startNATS(t)
	resolver := buildResolver([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "p1",
			Writable: true, DeviceRef: "dev:1"},
	})
	d := dispatch.New(nc, resolver, 2*time.Second)

	sub, _ := nc.Subscribe("cmd.sim.sim-01", func(msg *nats.Msg) {
		reply := dispatch.ConnectorReply{Success: false, Response: "device_error: write rejected"}
		data, _ := json.Marshal(reply)
		_ = msg.Respond(data)
	})
	t.Cleanup(func() { sub.Unsubscribe() })

	result := d.Execute(context.Background(), &pb.ControlCommand{
		ControlId: "ctrl-5", PointId: "p1", PresentValue: 1.0,
	})
	assert.False(t, result.Success)
	assert.Contains(t, result.Response, "device_error")
}

func TestDispatch_DedupWindow(t *testing.T) {
	nc := startNATS(t)
	resolver := buildResolver([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "p1",
			Writable: true, DeviceRef: "dev:1"},
	})
	d := dispatch.New(nc, resolver, 2*time.Second)

	writeCount := 0
	sub, _ := nc.Subscribe("cmd.sim.sim-01", func(msg *nats.Msg) {
		writeCount++
		reply := dispatch.ConnectorReply{Success: true, Response: "ok"}
		data, _ := json.Marshal(reply)
		_ = msg.Respond(data)
	})
	t.Cleanup(func() { sub.Unsubscribe() })

	cmd := &pb.ControlCommand{ControlId: "ctrl-dup", PointId: "p1", PresentValue: 1.0}
	r1 := d.Execute(context.Background(), cmd)
	r2 := d.Execute(context.Background(), cmd) // duplicate

	assert.True(t, r1.Success)
	assert.True(t, r2.Success, "duplicate must still return a result")
	assert.Equal(t, 1, writeCount, "device must be written exactly once")
}

// ── helpers ──────────────────────────────────────────────────────────────────

func startNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &server.Options{Port: -1}
	ns, err := server.NewServer(opts)
	require.NoError(t, err)
	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second))
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func buildResolver(entries []pointlist.Entry) *pointlist.SyncedResolver {
	r := pointlist.NewSynced(nil)
	r.Update(entries)
	return r
}

func TestDispatch_Release(t *testing.T) {
	nc := startNATS(t)
	entries := []pointlist.Entry{
		{PointID: "release", ReleaseOf: "target", Writable: true},
		{PointID: "target", ConnectorID: "bacnet-01", Protocol: "bacnet", LocalID: "analogOutput,37", DeviceRef: "device-1", Writable: true},
	}
	resolver := buildResolver(entries)
	received := make(chan map[string]any, 10)
	_, err := nc.Subscribe("cmd.bacnet.bacnet-01", func(msg *nats.Msg) {
		var req map[string]any
		_ = json.Unmarshal(msg.Data, &req)
		received <- req
		_ = msg.Respond([]byte(`{"success":true,"response":"ok"}`))
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	d := dispatch.New(nc, resolver, time.Second)
	cmd := &pb.ControlCommand{ControlId: "release-1", PointId: "release", PresentValue: 1, Priority: 8}
	require.True(t, d.Execute(context.Background(), cmd).Success)
	require.True(t, d.Execute(context.Background(), cmd).Success)
	req := <-received
	assert.Equal(t, true, req["release"])
	assert.Equal(t, "analogOutput,37", req["local_id"])
	assert.Equal(t, "device-1", req["device_ref"])
	assert.Equal(t, float64(8), req["priority"])
	assert.Empty(t, received)
	// Ordinary write wire must still contain value, including zero, and no release key.
	result := d.Execute(context.Background(), &pb.ControlCommand{ControlId: "normal", PointId: "target"})
	require.True(t, result.Success)
	req = <-received
	assert.Equal(t, float64(0), req["value"])
	assert.NotContains(t, req, "release")
}

func TestDispatch_ReleaseValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    float64
		priority int32
		target   pointlist.Entry
		response string
	}{
		{"zero", 0, 8, pointlist.Entry{PointID: "target", Writable: true, Protocol: "bacnet"}, "bad_request"},
		{"priority", 1, 17, pointlist.Entry{PointID: "target", Writable: true, Protocol: "bacnet"}, "bad_request"},
		{"missing", 1, 8, pointlist.Entry{}, "bad_request"},
		{"readonly", 1, 8, pointlist.Entry{PointID: "target"}, "not_writable"},
		{"unsupported", 1, 8, pointlist.Entry{PointID: "target", Writable: true, Protocol: "mqtt"}, "bad_request"},
		{"chain", 1, 8, pointlist.Entry{PointID: "target", Writable: true, ReleaseOf: "another"}, "bad_request"},
		{"no_connector", 1, 8, pointlist.Entry{PointID: "target", Writable: true, Protocol: "bacnet", LocalID: "analogOutput,37"}, "bad_request"},
		{"no_local_id", 1, 8, pointlist.Entry{PointID: "target", Writable: true, Protocol: "bacnet", ConnectorID: "bacnet-01"}, "bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := buildResolver([]pointlist.Entry{{PointID: "release", ReleaseOf: "target", Writable: true}, tc.target})
			result := dispatch.New(nil, r, time.Second).Execute(context.Background(), &pb.ControlCommand{ControlId: tc.name, PointId: "release", PresentValue: tc.value, Priority: tc.priority})
			assert.False(t, result.Success)
			assert.Equal(t, tc.response, result.Response)
		})
	}
}
