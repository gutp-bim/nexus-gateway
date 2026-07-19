// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/retry"
)

// statusReportInterval is how often the agent reports its applied point-list revision up the egress
// stream (#230 Phase 2b). Kept coarse — this is observability, and Building OS caches the last value
// between reports, so it need not match the bridge's connection-heartbeat cadence.
const statusReportInterval = 20 * time.Second

// Executor dispatches a ControlCommand and returns the result.
// Satisfied by *dispatch.Dispatcher.
type Executor interface {
	Execute(ctx context.Context, cmd *pb.ControlCommand) *pb.ControlResult
}

// RevisionProvider reports the point-list ETag the gateway currently has applied. Satisfied by
// *pointsync.Loop. Used to populate EgressUp.Status.applied_revision so Building OS can surface
// pointlist sync state on the same egress stream (ADR-0004 option A).
type RevisionProvider interface {
	AppliedRevision() string
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
	revalidate chan<- struct{}  // optional; nil = ignore PointListUpdate
	rev        RevisionProvider // optional; nil = do not report applied-revision status
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

// WithRevisionProvider attaches a RevisionProvider so the agent reports its applied point-list ETag
// via EgressUp.Status (#230 Phase 2b). Returns a for chaining; nil disables status reporting.
func (a *Agent) WithRevisionProvider(rp RevisionProvider) *Agent {
	a.rev = rp
	return a
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

	// gRPC forbids concurrent Send on one stream. The recv loop sends ControlResults and the status
	// reporter goroutine sends Status frames, so serialize every up-frame through one mutex.
	var sendMu sync.Mutex
	send := func(up *pb.EgressUp) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(up)
	}

	if err := send(&pb.EgressUp{M: &pb.EgressUp_Hello{
		Hello: &pb.Hello{GatewayId: a.gatewayID},
	}}); err != nil {
		return err
	}

	// Report the applied point-list revision now and then periodically, until this stream ends
	// (#230 Phase 2b). Best-effort: a send error just stops the reporter — the recv loop below
	// returns the real stream error that drives reconnect.
	if a.rev != nil {
		streamCtx, cancelReporter := context.WithCancel(ctx)
		defer cancelReporter()
		go a.reportStatus(streamCtx, send)
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
			if err := send(&pb.EgressUp{M: &pb.EgressUp_Result{Result: result}}); err != nil {
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

// reportStatus sends the current applied point-list revision up the stream immediately and then on
// statusReportInterval, until ctx is cancelled (the stream ended). Send is the serialized sender from
// runStream. Errors stop the reporter; the recv loop owns reconnect.
func (a *Agent) reportStatus(ctx context.Context, send func(*pb.EgressUp) error) {
	report := func() bool {
		up := &pb.EgressUp{M: &pb.EgressUp_Status{Status: &pb.GatewayStatus{AppliedRevision: a.rev.AppliedRevision()}}}
		if err := send(up); err != nil {
			slog.Debug("egress: status report send failed", "err", err)
			return false
		}
		return true
	}

	if !report() {
		return
	}
	tick := time.NewTicker(statusReportInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !report() {
				return
			}
		}
	}
}
