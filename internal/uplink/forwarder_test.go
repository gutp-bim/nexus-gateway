// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package uplink_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/storeforward"
	"nexus-gateway/internal/uplink"
)

// fakeSink is an in-memory FrameSink: it records the frames it was asked to send
// and returns a configurable accepted-count (and optional errors) on Checkpoint.
type fakeSink struct {
	mu        sync.Mutex
	sent      []*pb.TelemetryFrame
	accepted  int64
	sendErr   error
	ckptErr   error
	ckptCalls int
}

func (s *fakeSink) Send(_ context.Context, frame *pb.TelemetryFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, frame)
	return nil
}

func (s *fakeSink) Checkpoint(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ckptCalls++
	if s.ckptErr != nil {
		return 0, s.ckptErr
	}
	return s.accepted, nil
}

func (s *fakeSink) sentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func newBuf(t *testing.T) *storeforward.Buffer {
	t.Helper()
	buf, err := storeforward.Open(filepath.Join(t.TempDir(), "sf.db"), 1000)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })
	return buf
}

func writeFrames(t *testing.T, buf *storeforward.Buffer, pointIDs ...string) {
	t.Helper()
	for _, pid := range pointIDs {
		require.NoError(t, buf.Write(&pb.TelemetryFrame{GatewayId: "gw-1", PointId: pid, Value: 1.0, Timestamp: "2026-01-01T00:00:00Z"}))
	}
}

// On the happy path the Forwarder sends every buffered frame and, when the whole
// batch is accepted, advances the persisted cursor past it with no drift.
func TestForwarder_AdvancesCursorOnFullAck(t *testing.T) {
	buf := newBuf(t)
	writeFrames(t, buf, "p1", "p2", "p3")
	sink := &fakeSink{accepted: 3}

	fwd := uplink.NewForwarder(buf, sink, uplink.Config{CheckpointSize: 3, CheckpointAge: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fwd.Run(ctx) //nolint:errcheck

	assert.Eventually(t, func() bool { return buf.Cursor() == 3 }, 2*time.Second, 20*time.Millisecond)
	assert.Equal(t, 3, sink.sentCount())
	assert.Empty(t, buf.Drifts())
}

// When the server accepts fewer frames than were sent (Point List ⇄ twin drift),
// the cursor still advances past the whole batch (best-effort, no resend) and the
// rejected frame is recorded as per-point_id drift.
func TestForwarder_RecordsDriftOnPartialAck(t *testing.T) {
	buf := newBuf(t)
	writeFrames(t, buf, "p1", "p2", "p3")
	sink := &fakeSink{accepted: 2} // only 2 of 3 accepted

	fwd := uplink.NewForwarder(buf, sink, uplink.Config{CheckpointSize: 3, CheckpointAge: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fwd.Run(ctx) //nolint:errcheck

	assert.Eventually(t, func() bool { return buf.Cursor() == 3 }, 2*time.Second, 20*time.Millisecond)
	drifts := buf.Drifts()
	var total int64
	for _, v := range drifts {
		total += v
	}
	assert.Equal(t, int64(1), total, "exactly one frame should be recorded as drift")
}

// A frame written after Run has started is forwarded promptly via the buffer's
// write signal — faster than the 1s backstop, proving the notify path (#71).
func TestForwarder_ForwardsWriteAfterStartViaNotify(t *testing.T) {
	buf := newBuf(t)
	sink := &fakeSink{accepted: 1}

	fwd := uplink.NewForwarder(buf, sink, uplink.Config{CheckpointSize: 1, CheckpointAge: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fwd.Run(ctx) //nolint:errcheck

	time.Sleep(20 * time.Millisecond) // let Run reach its select
	writeFrames(t, buf, "p1")

	// 700ms < the 1s backstop, so only the write-notify path can satisfy this.
	assert.Eventually(t, func() bool { return buf.Cursor() == 1 }, 700*time.Millisecond, 10*time.Millisecond)
	assert.Equal(t, 1, sink.sentCount())
}

// A zero-value Config must not panic: NewForwarder clamps non-positive
// CheckpointSize/CheckpointAge to the defaults (time.NewTicker panics on <= 0).
func TestForwarder_ZeroConfigClampsToDefaults(t *testing.T) {
	buf := newBuf(t)
	writeFrames(t, buf, "p1")
	sink := &fakeSink{accepted: 1}

	fwd := uplink.NewForwarder(buf, sink, uplink.Config{}) // all-zero config
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	require.NotPanics(t, func() { fwd.Run(ctx) })
	// The frame is sent immediately on poll even though the default 5s/1000 checkpoint
	// has not yet fired, proving Run reached its loop instead of panicking on NewTicker(0).
	assert.Equal(t, 1, sink.sentCount())
}

// On a full-ack checkpoint the Forwarder records the sent frames and the
// checkpoint into the Buffer (the single store-and-forward metrics source).
func TestForwarder_RecordsSentAndCheckpoint(t *testing.T) {
	buf := newBuf(t)
	writeFrames(t, buf, "p1", "p2", "p3")
	sink := &fakeSink{accepted: 3}

	fwd := uplink.NewForwarder(buf, sink, uplink.Config{CheckpointSize: 3, CheckpointAge: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fwd.Run(ctx) //nolint:errcheck

	assert.Eventually(t, func() bool { return buf.Checkpoints() == 1 }, 2*time.Second, 20*time.Millisecond)
	assert.Equal(t, int64(3), buf.Sent(), "all 3 frames recorded as sent")
	assert.Equal(t, int64(0), buf.SendErrors())
}

// A send failure records a send error and does not count a checkpoint.
func TestForwarder_RecordsSendError(t *testing.T) {
	buf := newBuf(t)
	writeFrames(t, buf, "p1")
	sink := &fakeSink{sendErr: errors.New("stream broken")}

	fwd := uplink.NewForwarder(buf, sink, uplink.Config{CheckpointSize: 3, CheckpointAge: time.Hour})
	err := fwd.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, int64(1), buf.SendErrors(), "the send failure is metered")
	assert.Equal(t, int64(0), buf.Checkpoints(), "no checkpoint on a failed session")
}

// A send failure before any checkpoint must leave the cursor un-advanced so the
// un-acked batch is replayed on reconnect.
func TestForwarder_SendErrorDoesNotAdvanceCursor(t *testing.T) {
	buf := newBuf(t)
	writeFrames(t, buf, "p1", "p2", "p3")
	sink := &fakeSink{sendErr: errors.New("stream broken")}

	fwd := uplink.NewForwarder(buf, sink, uplink.Config{CheckpointSize: 3, CheckpointAge: time.Hour})
	err := fwd.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, int64(0), buf.Cursor(), "cursor must not advance when the send fails")
}

// A checkpoint (CloseAndRecv) failure must also leave the cursor un-advanced.
func TestForwarder_CheckpointErrorDoesNotAdvanceCursor(t *testing.T) {
	buf := newBuf(t)
	writeFrames(t, buf, "p1", "p2", "p3")
	sink := &fakeSink{ckptErr: errors.New("recv failed")}

	fwd := uplink.NewForwarder(buf, sink, uplink.Config{CheckpointSize: 3, CheckpointAge: time.Hour})
	err := fwd.Run(context.Background())
	require.Error(t, err)
	assert.Equal(t, int64(0), buf.Cursor(), "cursor must not advance when the checkpoint fails")
}
