// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package uplink

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/metrics"
	"nexus-gateway/internal/storeforward"
)

// FrameSink is the transport seam for the telemetry uplink. Frames are sent one
// at a time (immediately as they arrive, ADR-0002); Checkpoint half-closes the
// current batch and returns the cumulative accepted count, after which the sink
// is ready to start a fresh batch. The gRPC client-streaming transport is one
// adapter; tests inject an in-memory fake.
type FrameSink interface {
	Send(ctx context.Context, frame *pb.TelemetryFrame) error
	Checkpoint(ctx context.Context) (accepted int64, err error)
}

// Forwarder owns the best-effort store-and-forward delivery policy (ADR-0002):
// read frames from the Buffer, send them through a FrameSink immediately, and on
// every CheckpointSize frames or CheckpointAge — whichever first — half-close to
// collect the StreamAck, advance the cursor past the whole batch (never resend
// rejects), and record any accepted<sent shortfall as per-point_id drift.
//
// It depends only on the Buffer and the FrameSink, so the whole policy is testable
// in-process with no gRPC stack.
type Forwarder struct {
	buf  *storeforward.Buffer
	sink FrameSink
	cfg  Config
}

// NewForwarder creates a Forwarder over buf, delivering through sink under cfg.
// Non-positive CheckpointSize/CheckpointAge are clamped to DefaultConfig: a zero
// CheckpointAge would panic time.NewTicker, and a zero CheckpointSize would
// checkpoint after every frame (one StreamAck round-trip per frame).
func NewForwarder(buf *storeforward.Buffer, sink FrameSink, cfg Config) *Forwarder {
	if cfg.CheckpointSize <= 0 {
		cfg.CheckpointSize = DefaultConfig.CheckpointSize
	}
	if cfg.CheckpointAge <= 0 {
		cfg.CheckpointAge = DefaultConfig.CheckpointAge
	}
	return &Forwarder{buf: buf, sink: sink, cfg: cfg}
}

// Run drives one forwarding session until ctx is cancelled (returns nil) or the
// sink fails (returns the error so the caller can reconnect). On a sink failure
// the cursor is left un-advanced, so the un-acked batch is replayed on the next
// session — the bounded duplicate window of ADR-0002.
func (f *Forwarder) Run(ctx context.Context) error {
	cursor := f.buf.Cursor()
	tick := time.NewTicker(f.cfg.CheckpointAge)
	defer tick.Stop()
	// The primary trigger is the buffer's write signal (#71). The backstop is a
	// low-frequency safety net for frames written before Run started or a
	// coalesced/missed signal — not the hot path.
	backstop := time.NewTicker(time.Second)
	defer backstop.Stop()
	notify := f.buf.WriteNotify()

	var batch []storeforward.SentFrame

	checkpoint := func() error {
		if len(batch) == 0 {
			return nil
		}
		sent := int64(len(batch))
		accepted, err := f.sink.Checkpoint(ctx)
		if err != nil {
			f.buf.RecordSendError()
			metrics.SetUplinkConnected(false)
			return fmt.Errorf("checkpoint: %w", err)
		}
		f.buf.RecordSent(sent)
		f.buf.RecordCheckpoint()
		// A completed ack round-trip is the definitive "uplink healthy" signal (#23).
		// An idle gateway (no frames to checkpoint) holds the last-known state.
		metrics.SetUplinkConnected(true)
		newCursor, drifts := storeforward.ApplyAck(batch, accepted)
		for pointID, delta := range drifts {
			f.buf.RecordDrift(pointID, delta)
		}
		if len(drifts) > 0 {
			slog.Warn("ingress: drift", "sent", len(batch), "accepted", accepted, "lost", len(drifts))
		}
		// Advance past the whole batch regardless of accepted (best-effort, ADR-0002).
		cursor = newCursor
		if err := f.buf.Advance(cursor); err != nil {
			return fmt.Errorf("advance cursor: %w", err)
		}
		batch = batch[:0]
		tick.Reset(f.cfg.CheckpointAge)
		return nil
	}

	// drain sends every frame currently past the cursor, checkpointing on size.
	drain := func() error {
		for {
			frames, err := f.buf.ReadBatch(cursor, 32)
			if err != nil {
				slog.Warn("ingress: buffer read error", "err", err)
				return nil
			}
			if len(frames) == 0 {
				return nil
			}
			for _, sf := range frames {
				if err := f.sink.Send(ctx, sf.Frame); err != nil {
					f.buf.RecordSendError()
					metrics.SetUplinkConnected(false)
					return fmt.Errorf("send: %w", err)
				}
				batch = append(batch, storeforward.SentFrame{Seq: sf.Seq, PointID: sf.Frame.PointId})
				cursor = sf.Seq
				if len(batch) >= f.cfg.CheckpointSize {
					if err := checkpoint(); err != nil {
						return err
					}
				}
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			if len(batch) > 0 {
				_, _ = f.sink.Checkpoint(ctx)
			}
			return nil

		case <-tick.C:
			if err := checkpoint(); err != nil {
				return err
			}

		case <-notify:
			if err := drain(); err != nil {
				return err
			}

		case <-backstop.C:
			if err := drain(); err != nil {
				return err
			}
		}
	}
}
