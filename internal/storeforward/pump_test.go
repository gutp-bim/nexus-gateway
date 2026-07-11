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
		Frame: &pb.TelemetryFrame{GatewayId: "gw-1", PointId: pointID, Value: 1.5, Timestamp: "2026-01-01T00:00:00Z"},
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
