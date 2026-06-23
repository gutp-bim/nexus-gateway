// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/dispatch"
	"nexus-gateway/internal/egress"
	"nexus-gateway/internal/pointlist"
)

// TestControl_HappyPath: command arrives → sim connector updates → ControlResult success.
func TestControl_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ns := startEmbeddedNATS(t)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	// JetStream needed for EVENTS stream (shared infra)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "EVENTS", Subjects: []string{"evt.>"},
		Storage: jetstream.MemoryStorage,
	})
	require.NoError(t, err)

	// Resolver with one writable point
	resolver := pointlist.NewSynced(nil)
	resolver.Update([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "p1",
			Writable: true, DeviceRef: "dev:1"},
	})

	// Dispatcher
	d := dispatch.New(nc, resolver, 3*time.Second)

	// Register sim write handler on NATS
	var written atomic.Int64
	writeSub, err := nc.Subscribe("cmd.sim.sim-01", func(msg *nats.Msg) {
		written.Add(1)
		reply := dispatch.ConnectorReply{Success: true, Response: "ok"}
		data, _ := json.Marshal(reply)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { writeSub.Unsubscribe() })

	// Start mock Egress BOS server
	results := make(chan *pb.ControlResult, 10)
	bosEgress := startMockEgress(t, results)

	// Start Egress agent
	agent := egress.New(bosEgress.addr, "gw-001", d, insecureCreds(), nil)
	go agent.Run(ctx)

	// Push command from mock server
	bosEgress.SendCommand(&pb.ControlCommand{
		ControlId: "c-1", PointId: "p1", PresentValue: 42.0, Priority: 8,
	})

	// Verify result
	select {
	case r := <-results:
		assert.True(t, r.Success)
		assert.Equal(t, "c-1", r.ControlId)
	case <-ctx.Done():
		t.Fatal("timeout waiting for ControlResult")
	}
	assert.EqualValues(t, 1, written.Load(), "sim connector must be written once")
}

// TestControl_NotWritable: read-only point → no device write, not_writable result.
func TestControl_NotWritable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ns := startEmbeddedNATS(t)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	resolver := pointlist.NewSynced(nil)
	resolver.Update([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "ro-point",
			Writable: false},
	})
	d := dispatch.New(nc, resolver, 2*time.Second)

	var written atomic.Bool
	sub, _ := nc.Subscribe("cmd.sim.sim-01", func(_ *nats.Msg) { written.Store(true) })
	t.Cleanup(func() { sub.Unsubscribe() })

	results := make(chan *pb.ControlResult, 5)
	bosEgress := startMockEgress(t, results)
	agent := egress.New(bosEgress.addr, "gw-001", d, insecureCreds(), nil)
	go agent.Run(ctx)

	bosEgress.SendCommand(&pb.ControlCommand{
		ControlId: "c-ro", PointId: "ro-point", PresentValue: 1.0,
	})

	select {
	case r := <-results:
		assert.False(t, r.Success)
		assert.Equal(t, "not_writable", r.Response)
	case <-ctx.Done():
		t.Fatal("timeout")
	}
	assert.False(t, written.Load(), "device must not be written for read-only point")
}

// TestControl_EgressReconnect: mock server restart → agent reconnects and resumes.
func TestControl_EgressReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ns := startEmbeddedNATS(t)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	resolver := pointlist.NewSynced(nil)
	resolver.Update([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "p1",
			Writable: true},
	})
	d := dispatch.New(nc, resolver, 2*time.Second)

	sub, _ := nc.Subscribe("cmd.sim.sim-01", func(msg *nats.Msg) {
		reply := dispatch.ConnectorReply{Success: true, Response: "ok"}
		data, _ := json.Marshal(reply)
		_ = msg.Respond(data)
	})
	t.Cleanup(func() { sub.Unsubscribe() })

	results := make(chan *pb.ControlResult, 10)
	bos := startMockEgress(t, results)
	agent := egress.New(bos.addr, "gw-001", d, insecureCreds(), nil)
	go agent.Run(ctx)

	// Send first command, verify it works
	bos.SendCommand(&pb.ControlCommand{ControlId: "c-pre", PointId: "p1", PresentValue: 1.0})
	select {
	case r := <-results:
		require.True(t, r.Success)
	case <-ctx.Done():
		t.Fatal("first command timed out")
	}

	// Stop BOS and restart it on same addr
	bos.srv.Stop()
	time.Sleep(100 * time.Millisecond)
	bos2 := restartMockEgress(t, bos.addr, results)

	// Agent should reconnect and accept commands again
	require.Eventually(t, func() bool {
		bos2.SendCommand(&pb.ControlCommand{ControlId: "c-post", PointId: "p1", PresentValue: 2.0})
		select {
		case r := <-results:
			return r.ControlId == "c-post"
		case <-time.After(500 * time.Millisecond):
			return false
		}
	}, 10*time.Second, 200*time.Millisecond, "agent must reconnect after BOS restart")
}

// ── Mock BOS Egress server ────────────────────────────────────────────────────

type mockEgressServer struct {
	pb.UnimplementedGatewayEgressServer
	commands chan *pb.ControlCommand
	results  chan *pb.ControlResult
}

func (s *mockEgressServer) Connect(stream pb.GatewayEgress_ConnectServer) error {
	// Receive Hello first
	_, err := stream.Recv()
	if err != nil {
		return err
	}
	// Collect results from the agent in a background goroutine
	s.connectWithResults(stream)
	// Send queued commands
	for {
		select {
		case cmd := <-s.commands:
			if err := stream.Send(&pb.EgressDown{M: &pb.EgressDown_Command{Command: cmd}}); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

type egressHandle struct {
	addr    string
	srv     *grpc.Server
	mock    *mockEgressServer
}

func (h *egressHandle) SendCommand(cmd *pb.ControlCommand) {
	h.mock.commands <- cmd
}

func startMockEgress(t *testing.T, results chan *pb.ControlResult) *egressHandle {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	mock := &mockEgressServer{
		commands: make(chan *pb.ControlCommand, 10),
		results:  results,
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayEgressServer(srv, mock)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	return &egressHandle{addr: lis.Addr().String(), srv: srv, mock: mock}
}

func restartMockEgress(t *testing.T, addr string, results chan *pb.ControlResult) *egressHandle {
	t.Helper()
	lis, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	mock := &mockEgressServer{
		commands: make(chan *pb.ControlCommand, 10),
		results:  results,
	}
	srv := grpc.NewServer()
	pb.RegisterGatewayEgressServer(srv, mock)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	return &egressHandle{addr: addr, srv: srv, mock: mock}
}

// mockEgressServer needs to forward results from the agent back to the test.
// We intercept via a side-channel: the real server reads ControlResult from the bidi stream.
// Rewrite the Connect method to also collect results.
func (s *mockEgressServer) connectWithResults(stream pb.GatewayEgress_ConnectServer) {
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetResult() != nil {
				s.results <- msg.GetResult()
			}
		}
	}()
}
