// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/retry"
)

// Executor dispatches a ControlCommand and returns the result.
// Satisfied by *dispatch.Dispatcher.
type Executor interface {
	Execute(ctx context.Context, cmd *pb.ControlCommand) *pb.ControlResult
}

// Agent connects to the Building OS GatewayEgress service, sends Hello, and
// dispatches incoming ControlCommands via the Executor (ADR-0004).
// On EgressDown.point_list_update it signals revalidate so the pointsync.Loop
// can immediately re-fetch the Point List (#224/push).
type Agent struct {
	addr       string
	gatewayID  string
	exec       Executor
	creds      credentials.TransportCredentials
	revalidate chan<- struct{} // optional; nil = ignore PointListUpdate
}

// New creates an Agent.
// revalidate is signalled (non-blocking) when EgressDown.point_list_update arrives;
// pass nil to ignore push notifications.
func New(addr, gatewayID string, exec Executor,
	creds credentials.TransportCredentials, revalidate chan<- struct{}) *Agent {
	return &Agent{
		addr:       addr,
		gatewayID:  gatewayID,
		exec:       exec,
		creds:      creds,
		revalidate: revalidate,
	}
}

// Run connects to BOS and processes messages until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) {
	conn, err := grpc.NewClient(a.addr, grpc.WithTransportCredentials(a.creds))
	if err != nil {
		slog.Error("egress: dial failed", "addr", a.addr, "err", err)
		return
	}
	defer conn.Close()

	client := pb.NewGatewayEgressClient(conn)

	bo := &retry.Backoff{Min: time.Second, Max: 60 * time.Second, Factor: 2.0}
	for ctx.Err() == nil {
		if err := a.runStream(ctx, client); err != nil && ctx.Err() == nil {
			slog.Warn("egress stream error, reconnecting", "err", err)
			bo.Wait(ctx) //nolint:errcheck // ctx cancel exits the outer loop
		} else {
			bo.Reset()
		}
	}
}

func (a *Agent) runStream(ctx context.Context, client pb.GatewayEgressClient) error {
	stream, err := client.Connect(ctx)
	if err != nil {
		return err
	}

	if err := stream.Send(&pb.EgressUp{M: &pb.EgressUp_Hello{
		Hello: &pb.Hello{GatewayId: a.gatewayID},
	}}); err != nil {
		return err
	}

	for {
		down, err := stream.Recv()
		if err != nil {
			return err
		}

		switch m := down.GetM().(type) {
		case *pb.EgressDown_Command:
			if m.Command == nil {
				continue
			}
			result := a.exec.Execute(ctx, m.Command)
			if err := stream.Send(&pb.EgressUp{M: &pb.EgressUp_Result{Result: result}}); err != nil {
				return err
			}

		case *pb.EgressDown_PointListUpdate:
			slog.Info("egress: point list update signal received",
				"gateway_id", m.PointListUpdate.GetGatewayId(),
				"revision", m.PointListUpdate.GetRevision())
			if a.revalidate != nil {
				select {
				case a.revalidate <- struct{}{}:
				default: // non-blocking: drop if channel is full
				}
			}
		}
	}
}
