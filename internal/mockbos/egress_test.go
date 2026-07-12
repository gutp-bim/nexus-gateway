// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mockbos_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/mockbos"
)

// startEgress serves the mock-bos egress stub on an ephemeral port and returns
// a connected client, mirroring the compose wiring (issue #74): the gateway
// dials GatewayEgress on the same mock-bos address as GatewayIngress.
func startEgress(t *testing.T) pb.GatewayEgressClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	pb.RegisterGatewayEgressServer(gs, mockbos.NewEgressServer())
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return pb.NewGatewayEgressClient(conn)
}

// TestEgress_HoldsStreamOpenAfterHello is the #74 regression guard: Connect must
// not fail with Unimplemented (the pre-fix behavior that made the gateway's
// egress agent log reconnect warnings forever); after Hello the stream stays
// open with no server-initiated close.
func TestEgress_HoldsStreamOpenAfterHello(t *testing.T) {
	client := startEgress(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Connect(ctx)
	require.NoError(t, err)

	require.NoError(t, stream.Send(&pb.EgressUp{
		M: &pb.EgressUp_Hello{Hello: &pb.Hello{GatewayId: "gw-test"}},
	}))

	// The stub never sends EgressDown, so Recv must still be blocked (stream
	// alive) after a grace period — not returning Unimplemented or EOF.
	recvErr := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		recvErr <- err
	}()
	select {
	case err := <-recvErr:
		t.Fatalf("stream closed unexpectedly: %v (code=%v)", err, status.Code(err))
	case <-time.After(300 * time.Millisecond):
		// still open — expected
	}

	// Client-initiated close is clean: server returns nil, client sees io.EOF.
	require.NoError(t, stream.CloseSend())
	select {
	case err := <-recvErr:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("want io.EOF on clean close, got %v (code=%v)", err, status.Code(err))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not return after CloseSend")
	}
}

// TestEgress_ToleratesNonHelloFirstMessage: a client that skips Hello (or sends
// a ControlResult first) must not crash the stub or get an error-closed stream.
func TestEgress_ToleratesNonHelloFirstMessage(t *testing.T) {
	client := startEgress(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Connect(ctx)
	require.NoError(t, err)

	require.NoError(t, stream.Send(&pb.EgressUp{
		M: &pb.EgressUp_Result{Result: &pb.ControlResult{ControlId: "c-1", Success: true, Response: "ok"}},
	}))
	require.NoError(t, stream.CloseSend())

	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v (code=%v)", err, status.Code(err))
	}
}
