// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package uplink

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/retry"
	"nexus-gateway/internal/storeforward"
)

// Config holds tunable checkpoint parameters.
type Config struct {
	CheckpointSize int
	CheckpointAge  time.Duration
}

// DefaultConfig is the production default: send immediately, checkpoint every 5s/1000 frames.
var DefaultConfig = Config{CheckpointSize: 1000, CheckpointAge: 5 * time.Second}

// Ingress streams TelemetryFrames from a storeforward.Buffer to the Building OS
// GatewayIngress service. Frames are sent immediately as they are read; the stream
// is half-closed every CheckpointSize frames or CheckpointAge (whichever comes first)
// to collect the StreamAck and advance the buffer cursor (ADR-0002).
type Ingress struct {
	addr      string
	gatewayID string
	buf       *storeforward.Buffer
	conn      *grpc.ClientConn
	cfg       Config
}

func NewIngress(_ context.Context, addr, gatewayID string, buf *storeforward.Buffer, cfg Config, creds credentials.TransportCredentials) (*Ingress, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc dial %s: %w", addr, err)
	}
	return &Ingress{addr: addr, gatewayID: gatewayID, buf: buf, conn: conn, cfg: cfg}, nil
}

// Run streams frames until ctx is cancelled. Reconnects on stream errors,
// replaying the un-acked batch on the next session (ADR-0002). The delivery
// policy lives in Forwarder; this method only owns reconnect + the gRPC transport.
func (u *Ingress) Run(ctx context.Context) {
	defer u.conn.Close()
	client := pb.NewGatewayIngressClient(u.conn)

	bo := &retry.Backoff{Min: time.Second, Max: 60 * time.Second, Factor: 2.0}
	for ctx.Err() == nil {
		fwd := NewForwarder(u.buf, &grpcSink{client: client}, u.cfg)
		if err := fwd.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("ingress stream error, reconnecting", "err", err)
			bo.Wait(ctx) //nolint:errcheck // ctx cancel exits the outer loop
		} else {
			bo.Reset()
		}
	}
}

// grpcSink adapts the Building OS GatewayIngress client-streaming RPC to FrameSink.
// The stream is opened lazily on the first frame to avoid holding an idle
// connection that server-side idle-timeout policies would tear down repeatedly,
// and is half-closed on Checkpoint to collect the cumulative StreamAck.
type grpcSink struct {
	client pb.GatewayIngressClient
	stream pb.GatewayIngress_StreamTelemetryClient
}

func (g *grpcSink) Send(ctx context.Context, frame *pb.TelemetryFrame) error {
	if g.stream == nil {
		stream, err := g.client.StreamTelemetry(ctx)
		if err != nil {
			return err
		}
		g.stream = stream
	}
	return g.stream.Send(frame)
}

func (g *grpcSink) Checkpoint(_ context.Context) (int64, error) {
	if g.stream == nil {
		return 0, nil
	}
	ack, err := g.stream.CloseAndRecv()
	g.stream = nil // force a lazy re-open on the next Send
	if err != nil {
		return 0, fmt.Errorf("checkpoint recv: %w", err)
	}
	return ack.Accepted, nil
}
