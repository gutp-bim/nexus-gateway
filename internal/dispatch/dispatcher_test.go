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
