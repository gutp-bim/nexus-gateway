// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package egress_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/egress"
)

// mockEgressServer implements GatewayEgressServer for tests.
type mockEgressServer struct {
	pb.UnimplementedGatewayEgressServer
	// downMsgs is the sequence of EgressDown messages pushed to the gateway.
	downMsgs []*pb.EgressDown
	// results collects ControlResults sent up by the agent.
	results chan *pb.ControlResult
}

func (s *mockEgressServer) Connect(stream pb.GatewayEgress_ConnectServer) error {
	// Receive Hello
	_, err := stream.Recv()
	if err != nil {
		return err
	}
	// Push all queued down messages
	for _, msg := range s.downMsgs {
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	// Collect results until stream closes
	for {
		up, err := stream.Recv()
		if err != nil {
			return nil
		}
		if r := up.GetResult(); r != nil {
			s.results <- r
		}
	}
}

func startMockEgress(t *testing.T, srv *mockEgressServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	pb.RegisterGatewayEgressServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

// mockExecutor records which ControlCommands were dispatched.
type mockExecutor struct {
	commands []*pb.ControlCommand
}

func (e *mockExecutor) Execute(_ context.Context, cmd *pb.ControlCommand) *pb.ControlResult {
	e.commands = append(e.commands, cmd)
	return &pb.ControlResult{ControlId: cmd.ControlId, Success: true, Response: "ok"}
}

func TestAgent_DispatchesControlCommand(t *testing.T) {
	exec := &mockExecutor{}
	srv := &mockEgressServer{
		results: make(chan *pb.ControlResult, 4),
		downMsgs: []*pb.EgressDown{
			{M: &pb.EgressDown_Command{Command: &pb.ControlCommand{
				ControlId: "cmd-1", PointId: "pt-a", PresentValue: 1.0,
			}}},
		},
	}
	addr := startMockEgress(t, srv)

	revalidate := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go egress.New(addr, "gw-test", exec, insecure.NewCredentials(), revalidate).Run(ctx)

	select {
	case r := <-srv.results:
		assert.Equal(t, "cmd-1", r.ControlId)
		assert.True(t, r.Success)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ControlResult")
	}
	require.Len(t, exec.commands, 1)
	assert.Equal(t, "pt-a", exec.commands[0].PointId)
}

func TestAgent_PointListUpdate_SignalsRevalidate(t *testing.T) {
	exec := &mockExecutor{}
	srv := &mockEgressServer{
		results: make(chan *pb.ControlResult, 4),
		downMsgs: []*pb.EgressDown{
			{M: &pb.EgressDown_PointListUpdate{PointListUpdate: &pb.PointListUpdate{
				GatewayId: "gw-test", Revision: "etag-new",
			}}},
		},
	}
	addr := startMockEgress(t, srv)

	revalidate := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go egress.New(addr, "gw-test", exec, insecure.NewCredentials(), revalidate).Run(ctx)

	select {
	case <-revalidate:
		// success — PointListUpdate caused the agent to signal revalidate
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for revalidate signal")
	}
	assert.Empty(t, exec.commands, "PointListUpdate must not be treated as a ControlCommand")
}
