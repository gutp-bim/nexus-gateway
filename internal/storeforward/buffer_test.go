// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/storeforward"
)

// A successful Write signals WriteNotify so the uplink Forwarder can react
// immediately instead of polling on a fixed tick (#71).
func TestBuffer_WriteNotifies(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	defer buf.Close()

	n := buf.WriteNotify()
	require.NoError(t, buf.Write(&pb.TelemetryFrame{GatewayId: "gw", PointId: "p1", Value: 1, Timestamp: "t"}))

	select {
	case <-n:
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not signal WriteNotify")
	}
}

func TestBuffer_WriteReadAdvance(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	defer buf.Close()

	frames := []*pb.TelemetryFrame{
		{GatewayId: "gw-1", PointId: "p1", Value: 1.0, Timestamp: "2024-01-01T00:00:00Z"},
		{GatewayId: "gw-1", PointId: "p2", Value: 2.0, Timestamp: "2024-01-01T00:00:01Z"},
		{GatewayId: "gw-1", PointId: "p3", Value: 3.0, Timestamp: "2024-01-01T00:00:02Z"},
	}
	for _, f := range frames {
		require.NoError(t, buf.Write(f))
	}

	// ReadBatch from 0 should return all 3 in order
	batch, err := buf.ReadBatch(0, 10)
	require.NoError(t, err)
	require.Len(t, batch, 3)
	assert.Equal(t, "p1", batch[0].Frame.PointId)
	assert.Equal(t, "p2", batch[1].Frame.PointId)
	assert.Equal(t, "p3", batch[2].Frame.PointId)
	assert.True(t, batch[0].Seq < batch[1].Seq && batch[1].Seq < batch[2].Seq)

	// Advance past first two; ReadBatch should return only third
	require.NoError(t, buf.Advance(batch[1].Seq))
	batch2, err := buf.ReadBatch(batch[1].Seq, 10)
	require.NoError(t, err)
	require.Len(t, batch2, 1)
	assert.Equal(t, "p3", batch2[0].Frame.PointId)
}

// The Buffer is the single store-and-forward metrics source: it counts frames
// written and frames dropped on overflow, and accumulates the uplink-side
// sent/checkpoint/send-error records the Forwarder feeds it.
func TestBuffer_Counters(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 3) // capacity=3
	require.NoError(t, err)
	defer buf.Close()

	for i := range 5 {
		require.NoError(t, buf.Write(&pb.TelemetryFrame{
			GatewayId: "gw-1", PointId: "p" + string(rune('0'+i)), Value: float64(i), Timestamp: "2024-01-01T00:00:00Z",
		}))
	}

	assert.Equal(t, int64(5), buf.Written(), "every successful Write counts")
	assert.Equal(t, int64(2), buf.Dropped(), "2 of 5 evicted by drop-oldest at capacity 3")
	assert.Equal(t, int64(3), buf.Depth(), "depth is bounded by capacity")

	// Uplink-side records (the Forwarder feeds these).
	buf.RecordSent(10)
	buf.RecordSent(5)
	buf.RecordCheckpoint()
	buf.RecordCheckpoint()
	buf.RecordSendError()

	assert.Equal(t, int64(15), buf.Sent())
	assert.Equal(t, int64(2), buf.Checkpoints())
	assert.Equal(t, int64(1), buf.SendErrors())
}

func TestBuffer_DropOldestOnOverflow(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 3) // capacity=3
	require.NoError(t, err)
	defer buf.Close()

	for i := range 5 {
		require.NoError(t, buf.Write(&pb.TelemetryFrame{
			GatewayId: "gw-1",
			PointId:   "p" + string(rune('0'+i)),
			Value:     float64(i),
			Timestamp: "2024-01-01T00:00:00Z",
		}))
	}

	// Only 3 frames remain; they must be the newest (p2, p3, p4)
	batch, err := buf.ReadBatch(0, 10)
	require.NoError(t, err)
	require.Len(t, batch, 3)
	assert.Equal(t, "p2", batch[0].Frame.PointId)
	assert.Equal(t, "p3", batch[1].Frame.PointId)
	assert.Equal(t, "p4", batch[2].Frame.PointId)
}

func TestBuffer_DriftCounter(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	defer buf.Close()

	require.NoError(t, buf.Write(&pb.TelemetryFrame{PointId: "temp", Value: 1.0, Timestamp: "t"}))
	require.NoError(t, buf.Write(&pb.TelemetryFrame{PointId: "temp", Value: 2.0, Timestamp: "t"}))
	require.NoError(t, buf.Write(&pb.TelemetryFrame{PointId: "hum", Value: 3.0, Timestamp: "t"}))

	batch, err := buf.ReadBatch(0, 10)
	require.NoError(t, err)
	require.Len(t, batch, 3)

	// Report drift: 2 sent for temp, 1 accepted (1 lost)
	buf.RecordDrift("temp", 1)
	// 1 sent for hum, 1 accepted (no drift)
	buf.RecordDrift("hum", 0)

	drifts := buf.Drifts()
	assert.Equal(t, int64(1), drifts["temp"])
	assert.Equal(t, int64(0), drifts["hum"])
}

func TestBuffer_Depth(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	defer buf.Close()

	assert.Equal(t, int64(0), buf.Depth())

	for range 3 {
		require.NoError(t, buf.Write(&pb.TelemetryFrame{PointId: "p1", Value: 1.0, Timestamp: "t"}))
	}
	assert.Equal(t, int64(3), buf.Depth())
}

// Depth must report the un-forwarded backlog (frames with seq > cursor), not the
// total rows physically retained by the ring buffer. Rows are kept after ack
// (only dropped on capacity overflow), so COUNT(*) over-reports and tracks
// written_total instead of the real backlog (#109).
func TestBuffer_DepthReflectsUnsentBacklog(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	defer buf.Close()

	for range 5 {
		require.NoError(t, buf.Write(&pb.TelemetryFrame{PointId: "p", Value: 1.0, Timestamp: "t"}))
	}
	assert.Equal(t, int64(5), buf.Depth(), "nothing acked yet: full backlog")

	batch, err := buf.ReadBatch(0, 10)
	require.NoError(t, err)
	require.Len(t, batch, 5)

	// Ack the first three (cursor advances). Depth must drop to the remaining
	// un-forwarded backlog, even though all 5 rows are still physically present.
	require.NoError(t, buf.Advance(batch[2].Seq))
	assert.Equal(t, int64(2), buf.Depth(), "depth = unsent backlog (seq > cursor), not row count")
}

// Concurrent writers (pump) racing a drain/cursor loop (uplink Forwarder) must
// not surface SQLITE_BUSY 'database is locked'. Regression for #109, where
// writer–cursor contention under high write rates stalled forwarding.
func TestBuffer_ConcurrentWritersNoLock(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100_000)
	require.NoError(t, err)
	defer buf.Close()

	const writers, perWriter = 8, 400 // 3200 writes total

	var mu sync.Mutex
	var errs []error
	record := func(err error) {
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}

	// Drain/cursor goroutine mimicking the single uplink Forwarder.
	stop := make(chan struct{})
	var drained atomic.Int64
	var drainWG sync.WaitGroup
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		var cursor int64
		for {
			select {
			case <-stop:
				return
			default:
			}
			frames, err := buf.ReadBatch(cursor, 64)
			record(err)
			if len(frames) > 0 {
				cursor = frames[len(frames)-1].Seq
				record(buf.Advance(cursor))
				drained.Add(int64(len(frames)))
			}
			_ = buf.Depth() // exercise the read path under contention
		}
	}()

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				record(buf.Write(&pb.TelemetryFrame{
					GatewayId: "gw", PointId: fmt.Sprintf("p%d-%d", w, i), Value: float64(i), Timestamp: "t",
				}))
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	drainWG.Wait()

	require.Empty(t, errs, "no SQLITE_BUSY under concurrent write + drain")
	assert.Equal(t, int64(writers*perWriter), buf.Written())
	assert.Positive(t, drained.Load(), "drain/cursor loop made progress (not a silent no-op)")
}

func TestBuffer_PersistsCursor(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/sf.db"

	buf, err := storeforward.Open(dbPath, 100)
	require.NoError(t, err)
	require.NoError(t, buf.Write(&pb.TelemetryFrame{PointId: "p1", Value: 1.0, Timestamp: "t"}))
	require.NoError(t, buf.Write(&pb.TelemetryFrame{PointId: "p2", Value: 2.0, Timestamp: "t"}))

	batch, err := buf.ReadBatch(0, 10)
	require.NoError(t, err)
	require.NoError(t, buf.Advance(batch[0].Seq))
	buf.Close()

	// Reopen: cursor should still be at batch[0].Seq
	buf2, err := storeforward.Open(dbPath, 100)
	require.NoError(t, err)
	defer buf2.Close()

	cursor := buf2.Cursor()
	assert.Equal(t, batch[0].Seq, cursor)

	// Read from cursor: should return only p2
	batch2, err := buf2.ReadBatch(cursor, 10)
	require.NoError(t, err)
	require.Len(t, batch2, 1)
	assert.Equal(t, "p2", batch2[0].Frame.PointId)
}
