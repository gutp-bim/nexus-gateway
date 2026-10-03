// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/storeforward"
)

// fakeAckNaker records how the Pump acknowledged a frame's source message.
type fakeAckNaker struct {
	mu       sync.Mutex
	acked    bool
	naked    bool
	nakDelay time.Duration
}

func (f *fakeAckNaker) Ack() error { f.mu.Lock(); f.acked = true; f.mu.Unlock(); return nil }
func (f *fakeAckNaker) NakWithDelay(d time.Duration) error {
	f.mu.Lock()
	f.naked, f.nakDelay = true, d
	f.mu.Unlock()
	return nil
}
func (f *fakeAckNaker) state() (ack, nak bool, delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acked, f.naked, f.nakDelay
}

func frameMsg(pointID string, ack storeforward.AckNaker) storeforward.FrameMsg {
	return storeforward.FrameMsg{
		Frame: &pb.TelemetryFrame{GatewayId: "gw-1", PointId: pointID, Value: &pb.TelemetryFrame_ValueNum{ValueNum: 1.5}, Timestamp: "2026-01-01T00:00:00Z"},
		Msg:   ack,
	}
}

// The Pump acks a frame's source message only AFTER a durable buffer write (#28),
// so a JetStream event is never acked before it is safely persisted.
func TestPump_AcksAfterDurableWrite(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })

	ack := &fakeAckNaker{}
	src := make(chan storeforward.FrameMsg, 1)
	src <- frameMsg("p1", ack)
	close(src) // Pump drains the buffered item then returns on the closed channel

	storeforward.Pump(context.Background(), src, buf)

	acked, naked, _ := ack.state()
	assert.True(t, acked, "a durably-written frame must be acked")
	assert.False(t, naked)
	assert.Equal(t, int64(1), buf.Written())
	assert.Equal(t, int64(0), buf.WriteErrors())
}

// On a buffer write failure the Pump must NOT ack (so JetStream redelivers), NAK
// with a backoff delay, and count the write error distinctly (#28).
func TestPump_NaksAndCountsOnWriteError(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	buf.Close() // a closed DB makes every Write return an error

	ack := &fakeAckNaker{}
	src := make(chan storeforward.FrameMsg, 1)
	src <- frameMsg("p1", ack)
	close(src)

	storeforward.Pump(context.Background(), src, buf)

	acked, naked, delay := ack.state()
	assert.False(t, acked, "a frame that failed to persist must not be acked")
	assert.True(t, naked, "a failed write must be NAK'd for redelivery")
	assert.Greater(t, delay, time.Duration(0), "NAK carries a backoff delay")
	assert.Equal(t, int64(1), buf.WriteErrors())
}

// A redelivered copy of an already-written source message is acked but not
// buffered again (#186): without this the copy was written and forwarded twice.
func TestPump_RedeliveredSequenceIsAckedNotRewritten(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })

	first, redelivered, other := &fakeAckNaker{}, &fakeAckNaker{}, &fakeAckNaker{}
	src := make(chan storeforward.FrameMsg, 3)
	for _, fm := range []struct {
		seq uint64
		ack storeforward.AckNaker
	}{{7, first}, {7, redelivered}, {8, other}} {
		m := frameMsg("p1", fm.ack)
		m.Seq = fm.seq
		src <- m
	}
	close(src)

	storeforward.Pump(context.Background(), src, buf)

	assert.Equal(t, int64(2), buf.Written(), "seq 7 is written once, seq 8 once")
	assert.Equal(t, int64(1), buf.Duplicates())
	for name, a := range map[string]*fakeAckNaker{"first": first, "redelivered": redelivered, "other": other} {
		acked, naked, _ := a.state()
		assert.True(t, acked, "%s must be acked", name)
		assert.False(t, naked, "%s must not be NAK'd", name)
	}
}

// Seq 0 means "unknown" and must never be treated as a duplicate of itself.
func TestPump_ZeroSequenceIsNotDeduplicated(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })

	src := make(chan storeforward.FrameMsg, 2)
	src <- frameMsg("p1", &fakeAckNaker{})
	src <- frameMsg("p1", &fakeAckNaker{})
	close(src)

	storeforward.Pump(context.Background(), src, buf)

	assert.Equal(t, int64(2), buf.Written())
	assert.Equal(t, int64(0), buf.Duplicates())
}

// A failed write must not mark the sequence as written: the NAK'd redelivery has
// to be written, not skipped as a duplicate (#28 still holds).
func TestPump_FailedWriteDoesNotConsumeSequence(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	buf.Close() // every Write fails

	src := make(chan storeforward.FrameMsg, 2)
	for range 2 {
		m := frameMsg("p1", &fakeAckNaker{})
		m.Seq = 5
		src <- m
	}
	close(src)

	storeforward.Pump(context.Background(), src, buf)

	assert.Equal(t, int64(2), buf.WriteErrors(), "both attempts reach the write path")
	assert.Equal(t, int64(0), buf.Duplicates())
}
