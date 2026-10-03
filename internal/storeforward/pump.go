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

// dedupWindowSeqs is how many consecutive stream sequence numbers the Pump can
// tell apart: a redelivered message is recognised as long as its original was
// written within the last dedupWindowSeqs stream messages.
//
// What it has to cover is the redelivery delay, one AckWait (2 min, set in
// internal/normalizer): an ack lost after the write brings the message back that
// much later. At 2 min the window therefore covers sustained rates up to
// 2^21 / 120 s ≈ 17,500 messages/s, for 256 KiB (a bitmap indexed by sequence
// value, not a set of recent entries). Beyond that rate the dedup is best-effort.
const dedupWindowSeqs = 1 << 21

// seqWindow remembers which stream sequences in [base, base+n) were written: a
// bitmap whose bit for sequence s is s mod n, so lookups and inserts are O(1) and
// sliding forward only clears the bits of sequences that fell out. A sequence
// older than base can no longer be judged: it reads as unseen and is not recorded.
// It is confined to the Pump goroutine, so it needs no locking.
//
// The window is deliberately in memory: the Pump writes then acks one message at
// a time, so a crash can leave at most one written-but-unacked message, and the
// at-most-one duplicate after a restart is within the at-least-once contract of
// #28.
type seqWindow struct {
	bits []uint64
	n    uint64 // window span in sequences; a power of two
	base uint64 // lowest sequence the window covers
}

// newSeqWindow returns a window spanning at least size sequences (rounded up to a
// power of two, minimum 64).
func newSeqWindow(size int) *seqWindow {
	n := uint64(64)
	for n < uint64(size) {
		n <<= 1
	}
	return &seqWindow{bits: make([]uint64, n/64), n: n}
}

func (w *seqWindow) contains(seq uint64) bool {
	if seq < w.base || seq-w.base >= w.n {
		return false
	}
	i := seq & (w.n - 1)
	return w.bits[i>>6]&(1<<(i&63)) != 0
}

func (w *seqWindow) add(seq uint64) {
	if seq < w.base {
		return // older than the window: cannot be recorded
	}
	if seq-w.base >= w.n {
		w.slide(seq - w.n + 1) // make seq the newest covered sequence
	}
	i := seq & (w.n - 1)
	w.bits[i>>6] |= 1 << (i & 63)
}

// slide moves base forward to newBase, forgetting the sequences it passes.
func (w *seqWindow) slide(newBase uint64) {
	if newBase-w.base >= w.n {
		clear(w.bits)
	} else {
		for s := w.base; s < newBase; s++ {
			i := s & (w.n - 1)
			w.bits[i>>6] &^= 1 << (i & 63)
		}
	}
	w.base = newBase
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
	written := newSeqWindow(dedupWindowSeqs)
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
