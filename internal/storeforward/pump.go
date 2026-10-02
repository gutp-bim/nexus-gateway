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
	// Seq is the source message's JetStream stream sequence, the Pump's idempotency
	// key: a redelivered copy of an already-written message is acked, not written
	// again (#186). 0 means unknown and disables the check for this frame.
	Seq uint64
}

// dedupWindowSize is how many recently written stream sequences the Pump
// remembers. A redelivery arrives one AckWait (minutes) after the original
// delivery, so the window only has to span the messages written in that time;
// 64Ki entries (~1 MB) covers it at hundreds of messages per second.
const dedupWindowSize = 1 << 16

// seqWindow is a bounded set of recently written stream sequences: lookups are
// O(1) and, once full, adding evicts the oldest entry. It is confined to the Pump
// goroutine, so it needs no locking. The window is deliberately in memory: the
// Pump writes then acks one message at a time, so a crash can leave at most one
// written-but-unacked message, and the at-most-one duplicate after a restart is
// within the at-least-once contract of #28.
type seqWindow struct {
	seen map[uint64]struct{}
	ring []uint64 // insertion order; 0 marks an unused slot (Seq 0 is never stored)
	next int
}

func newSeqWindow(size int) *seqWindow {
	return &seqWindow{seen: make(map[uint64]struct{}, size), ring: make([]uint64, size)}
}

func (w *seqWindow) contains(seq uint64) bool {
	_, ok := w.seen[seq]
	return ok
}

func (w *seqWindow) add(seq uint64) {
	if old := w.ring[w.next]; old != 0 {
		delete(w.seen, old)
	}
	w.ring[w.next] = seq
	w.seen[seq] = struct{}{}
	w.next = (w.next + 1) % len(w.ring)
}

// Pump reads FrameMsgs from src and writes them to buf until ctx is done or src is
// closed. It acknowledges each source message ONLY after a successful durable
// write; a write failure is metered and the message is NAK'd for redelivery, so a
// full disk or SQLite error can no longer silently lose an already-acked frame.
//
// The write is idempotent per source message (#186): JetStream redelivers a
// message that waited past its ack deadline while the Pump was behind, and without
// this check the redelivered copy was buffered and forwarded a second time. A
// frame whose stream sequence was already written is acked and counted as a
// duplicate instead.
func Pump(ctx context.Context, src <-chan FrameMsg, buf *Buffer) {
	written := newSeqWindow(dedupWindowSize)
	for {
		select {
		case fm, ok := <-src:
			if !ok {
				return
			}
			if fm.Seq != 0 && written.contains(fm.Seq) {
				buf.RecordDuplicate()
				if fm.Msg != nil {
					_ = fm.Msg.Ack()
				}
				continue
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
			if fm.Seq != 0 {
				written.add(fm.Seq)
			}
			if fm.Msg != nil {
				_ = fm.Msg.Ack()
			}
		case <-ctx.Done():
			return
		}
	}
}
