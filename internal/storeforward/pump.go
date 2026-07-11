// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward

import (
	"context"
	"log/slog"
	"time"

	pb "nexus-gateway/gen"
)

// writeErrorNakDelay is how long a frame's source message is held before
// redelivery after a buffer write failure — a fixed backoff giving a transient
// condition (e.g. a full disk being cleared) time to recover before the retry.
const writeErrorNakDelay = 5 * time.Second

// AckNaker is the acknowledgement seam a FrameMsg carries: the durable-write
// outcome drives it. It is the subset of the upstream message the Pump needs, so
// a jetstream.Msg (and test doubles) satisfy it structurally without this
// low-level package importing NATS.
type AckNaker interface {
	Ack() error
	NakWithDelay(d time.Duration) error
}

// FrameMsg pairs a normalized frame with the acknowledgement controls of the
// source event, so acknowledgement can be deferred until AFTER the durable buffer
// write instead of firing when the frame is merely enqueued (#28).
type FrameMsg struct {
	Frame *pb.TelemetryFrame
	Msg   AckNaker
}

// Pump reads FrameMsgs from src and writes them to buf until ctx is done or src is
// closed. It acknowledges each source message ONLY after a successful durable
// write; a write failure is metered and the message is NAK'd for redelivery, so a
// full disk or SQLite error can no longer silently lose an already-acked frame.
func Pump(ctx context.Context, src <-chan FrameMsg, buf *Buffer) {
	for {
		select {
		case fm, ok := <-src:
			if !ok {
				return
			}
			if err := buf.Write(fm.Frame); err != nil {
				buf.RecordWriteError()
				// Attribute the loss to a specific point/gateway (matching the
				// Normalizer's attribution style) so a persistent write failure is
				// diagnosable (#25). Do NOT ack: NAK for redelivery so the event is
				// not lost while the write path is failing (#28).
				slog.Warn("storeforward: buffer write error — NAK for redelivery",
					"err", err, "point_id", fm.Frame.PointId, "gateway_id", fm.Frame.GatewayId)
				if fm.Msg != nil {
					_ = fm.Msg.NakWithDelay(writeErrorNakDelay)
				}
				continue
			}
			if fm.Msg != nil {
				_ = fm.Msg.Ack()
			}
		case <-ctx.Done():
			return
		}
	}
}
