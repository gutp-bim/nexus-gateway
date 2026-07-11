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

// forwardSession holds the per-connection cursor and in-flight (sent, not-yet-
// checkpointed) batch for one drain/checkpoint cycle. Extracting it lets both Run
// (the steady loop) and DrainOnce (the shutdown flush) share the same delivery
// policy while driving the sink with different contexts.
type forwardSession struct {
	f      *Forwarder
	cursor int64
	batch  []storeforward.SentFrame
}

func (f *Forwarder) newSession() *forwardSession {
	return &forwardSession{f: f, cursor: f.buf.Cursor()}
}

// checkpoint half-closes the current batch, records the ack, and advances the
// cursor past the whole batch (best-effort, ADR-0002). ctx drives the sink call,
// so the shutdown flush can pass a fresh context after the run ctx is cancelled.
func (s *forwardSession) checkpoint(ctx context.Context) error {
	if len(s.batch) == 0 {
		return nil
	}
	sent := int64(len(s.batch))
	accepted, err := s.f.sink.Checkpoint(ctx)
	if err != nil {
		s.f.buf.RecordSendError()
		metrics.SetUplinkConnected(false)
		return fmt.Errorf("checkpoint: %w", err)
	}
	s.f.buf.RecordSent(sent)
	s.f.buf.RecordAccepted(accepted)
	s.f.buf.RecordCheckpoint()
	// A completed ack round-trip is the definitive "uplink healthy" signal (#23).
	metrics.SetUplinkConnected(true)
	newCursor, drifts := storeforward.ApplyAck(s.batch, accepted)
	for pointID, delta := range drifts {
		s.f.buf.RecordDrift(pointID, delta)
	}
	if len(drifts) > 0 {
		slog.Warn("ingress: drift", "sent", len(s.batch), "accepted", accepted, "lost", len(drifts))
	}
	// Advance past the whole batch regardless of accepted (best-effort, ADR-0002).
	s.cursor = newCursor
	if err := s.f.buf.Advance(s.cursor); err != nil {
		return fmt.Errorf("advance cursor: %w", err)
	}
	s.batch = s.batch[:0]
	return nil
}

// drain sends every frame currently past the cursor, checkpointing on size.
func (s *forwardSession) drain(ctx context.Context) error {
	for {
		frames, err := s.f.buf.ReadBatch(s.cursor, 32)
		if err != nil {
			slog.Warn("ingress: buffer read error", "err", err)
			return nil
		}
		if len(frames) == 0 {
			return nil
		}
		for _, sf := range frames {
			if err := s.f.sink.Send(ctx, sf.Frame); err != nil {
				s.f.buf.RecordSendError()
				metrics.SetUplinkConnected(false)
				return fmt.Errorf("send: %w", err)
			}
			s.batch = append(s.batch, storeforward.SentFrame{Seq: sf.Seq, PointID: sf.Frame.PointId})
			s.cursor = sf.Seq
			if len(s.batch) >= s.f.cfg.CheckpointSize {
				if err := s.checkpoint(ctx); err != nil {
					return err
				}
			}
		}
	}
}

// Run drives one forwarding session until ctx is cancelled (returns nil) or the
// sink fails (returns the error so the caller can reconnect). On a sink failure
// the cursor is left un-advanced, so the un-acked batch is replayed on the next
// session — the bounded duplicate window of ADR-0002.
//
// On ctx cancel Run returns WITHOUT a final checkpoint: the sink's gRPC stream is
// bound to ctx and is already dead, so a checkpoint here would fail. The clean
// final flush is performed by the caller (Ingress) via DrainOnce on a fresh
// context and a fresh sink (#27).
func (f *Forwarder) Run(ctx context.Context) error {
	s := f.newSession()
	tick := time.NewTicker(f.cfg.CheckpointAge)
	defer tick.Stop()
	// The primary trigger is the buffer's write signal (#71). The backstop is a
	// low-frequency safety net for frames written before Run started or a
	// coalesced/missed signal — not the hot path.
	backstop := time.NewTicker(time.Second)
	defer backstop.Stop()
	notify := f.buf.WriteNotify()

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-tick.C:
			if err := s.checkpoint(ctx); err != nil {
				return err
			}

		case <-notify:
			if err := s.drain(ctx); err != nil {
				return err
			}

		case <-backstop.C:
			if err := s.drain(ctx); err != nil {
				return err
			}
		}
	}
}

// DrainOnce sends every frame currently past the cursor and checkpoints once,
// advancing the cursor. It is the clean-shutdown flush (#27): the caller passes a
// fresh, non-cancelled context (and a fresh sink, so a new stream is opened on
// that context) after the run ctx is cancelled, so the final ack round-trip
// completes and the replay window stays within the ADR-0002 checkpoint bound.
func (f *Forwarder) DrainOnce(ctx context.Context) error {
	s := f.newSession()
	if err := s.drain(ctx); err != nil {
		return err
	}
	return s.checkpoint(ctx)
}
