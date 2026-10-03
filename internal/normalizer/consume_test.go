// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package normalizer_test

import (
	"context"
	"encoding/json"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/common"
	"nexus-gateway/internal/metrics"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/storeforward"
)

// fakeMsg records which ack control the consume loop invoked.
type fakeMsg struct {
	data []byte
	seq  uint64 // stream sequence reported by Metadata
	// delivered is the delivery count reported by Metadata; 0 reads as a first delivery.
	delivered uint64
	mu        sync.Mutex
	ack       bool
	term      bool
	nak       bool
}

func (m *fakeMsg) Data() []byte { return m.data }
func (m *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) {
	d := max(m.delivered, 1)
	return &jetstream.MsgMetadata{Sequence: jetstream.SequencePair{Stream: m.seq}, NumDelivered: d}, nil
}
func (m *fakeMsg) Ack() error                       { m.mu.Lock(); m.ack = true; m.mu.Unlock(); return nil }
func (m *fakeMsg) Term() error                      { m.mu.Lock(); m.term = true; m.mu.Unlock(); return nil }
func (m *fakeMsg) Nak() error                       { m.mu.Lock(); m.nak = true; m.mu.Unlock(); return nil }
func (m *fakeMsg) NakWithDelay(time.Duration) error { return m.Nak() }
func (m *fakeMsg) acked() bool                      { m.mu.Lock(); defer m.mu.Unlock(); return m.ack }
func (m *fakeMsg) termed() bool                     { m.mu.Lock(); defer m.mu.Unlock(); return m.term }

// fakeSource yields preloaded batches once, then blocks for maxWait like a real
// JetStream pull consumer (so the consume loop does not busy-spin).
type fakeSource struct {
	mu      sync.Mutex
	batches [][]normalizer.EventMsg
}

func (s *fakeSource) Fetch(_ int, _ time.Duration) iter.Seq[normalizer.EventMsg] {
	return func(yield func(normalizer.EventMsg) bool) {
		s.mu.Lock()
		var b []normalizer.EventMsg
		if len(s.batches) > 0 {
			b = s.batches[0]
			s.batches = s.batches[1:]
		}
		s.mu.Unlock()
		if len(b) == 0 {
			time.Sleep(5 * time.Millisecond) // brief idle like a pull consumer; keeps join fast
			return
		}
		for _, m := range b {
			if !yield(m) {
				return
			}
		}
	}
}

// startNormalizer wires a Normalizer over src and guarantees the consume goroutine
// has fully exited before the test returns — so its process-global metric writes
// never overlap another test's before/after delta (see TestNormalizer_DropsAndMeters…).
func startNormalizer(t *testing.T, src normalizer.EventSource, r pointlist.Resolver) *normalizer.Normalizer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n := normalizer.NewWithSource(ctx, src, r, "gw-1")
	t.Cleanup(func() {
		cancel()
		for range n.Frames() { // drains until the goroutine closes the channel = joined
		}
	})
	return n
}

func eventJSON(t *testing.T, connectorID, localID string, value float64) []byte {
	t.Helper()
	b, err := json.Marshal(common.Event{ConnectorID: connectorID, LocalID: localID, Value: common.NumberValue(value), Timestamp: "2026-01-01T00:00:00Z"})
	require.NoError(t, err)
	return b
}

func resolverWith(entries ...pointlist.Entry) pointlist.Resolver {
	return pointlist.NewFixture(entries)
}

// A resolved Common Event becomes a TelemetryFrame. Its source message remains
// unacked until the downstream Pump has durably stored the frame.
func TestNormalizer_OKEmitsFrameWithoutEarlyAck(t *testing.T) {
	msg := &fakeMsg{data: eventJSON(t, "c1", "l1", 1.5)}
	src := &fakeSource{batches: [][]normalizer.EventMsg{{msg}}}
	r := resolverWith(pointlist.Entry{ConnectorID: "c1", LocalID: "l1", PointID: "p1"})

	n := startNormalizer(t, src, r)

	select {
	case f := <-n.Frames():
		assert.Equal(t, "p1", f.Frame.PointId)
		assert.Equal(t, "gw-1", f.Frame.GatewayId)
		assert.Equal(t, 1.5, f.Frame.GetValueNum())
	case <-time.After(2 * time.Second):
		t.Fatal("expected a TelemetryFrame")
	}
	assert.False(t, msg.acked(), "normalizer must not ack before durable storage")
}

// Unparseable payload → no frame, Term (drop-and-meter, ADR-0002).
func TestNormalizer_PoisonTermedNoFrame(t *testing.T) {
	msg := &fakeMsg{data: []byte("not json")}
	src := &fakeSource{batches: [][]normalizer.EventMsg{{msg}}}

	n := startNormalizer(t, src, resolverWith())

	assert.Eventually(t, msg.termed, time.Second, 10*time.Millisecond, "poison event must be Termed")
	select {
	case f := <-n.Frames():
		t.Fatalf("poison event must not emit a frame, got %v", f)
	case <-time.After(100 * time.Millisecond):
	}
}

// Unknown local_id → no frame, Term (point-list miss, ADR-0003).
func TestNormalizer_MissTermedNoFrame(t *testing.T) {
	msg := &fakeMsg{data: eventJSON(t, "c1", "unknown", 1.0)}
	src := &fakeSource{batches: [][]normalizer.EventMsg{{msg}}}
	r := resolverWith(pointlist.Entry{ConnectorID: "c1", LocalID: "l1", PointID: "p1"})

	n := startNormalizer(t, src, r)

	assert.Eventually(t, msg.termed, time.Second, 10*time.Millisecond, "miss event must be Termed")
	select {
	case f := <-n.Frames():
		t.Fatalf("miss event must not emit a frame, got %v", f)
	case <-time.After(100 * time.Millisecond):
	}
}

// countingSource yields up to max fresh, resolvable messages per Fetch and records
// how many it has handed out, so a test can see whether the consume loop pulled
// more than the hand-off channel can hold.
type countingSource struct {
	t       *testing.T
	mu      sync.Mutex
	yielded int
	maxSeen int
	calls   int
}

func (s *countingSource) Fetch(n int, _ time.Duration) iter.Seq[normalizer.EventMsg] {
	return func(yield func(normalizer.EventMsg) bool) {
		s.mu.Lock()
		s.maxSeen = max(s.maxSeen, n)
		s.calls++
		if s.calls == 1 {
			// An odd first batch keeps later batches from dividing the channel's
			// capacity evenly, so an unbounded fetch overshoots instead of fitting.
			n = min(n, 3)
		}
		s.mu.Unlock()
		for range n {
			s.mu.Lock()
			s.yielded++
			seq := uint64(s.yielded)
			s.mu.Unlock()
			if !yield(&fakeMsg{data: eventJSON(s.t, "c1", "l1", 1), seq: seq}) {
				return
			}
		}
	}
}

func (s *countingSource) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.yielded }

// The consume loop must not fetch more than the hand-off channel has room for:
// a message held in hand while the channel is full burns its ack deadline (#186).
func TestNormalizer_FetchBoundedByChannelCapacity(t *testing.T) {
	src := &countingSource{t: t}
	r := resolverWith(pointlist.Entry{ConnectorID: "c1", LocalID: "l1", PointID: "p1"})

	n := startNormalizer(t, src, r)
	_ = n // nobody reads Frames(): the channel fills and the loop must stall

	require.Eventually(t, func() bool { return src.count() >= 256 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(150 * time.Millisecond) // give an over-eager loop time to pull more

	assert.Equal(t, 256, src.count(), "exactly the channel's capacity is fetched, none held in hand")
	assert.LessOrEqual(t, src.maxSeen, 32)
}

// End to end: an original and its JetStream redelivery (same stream sequence) both
// queue up behind a slow Pump. The buffer must hold ONE frame, both messages must
// be acked, and the redelivery must be metered (#186).
func TestNormalizer_RedeliveryUnderBacklogIsBufferedOnce(t *testing.T) {
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)

	original := &fakeMsg{data: eventJSON(t, "c1", "l1", 1.5), seq: 42, delivered: 1}
	redelivered := &fakeMsg{data: eventJSON(t, "c1", "l1", 1.5), seq: 42, delivered: 2}
	src := &fakeSource{batches: [][]normalizer.EventMsg{{original}, {redelivered}}}
	r := resolverWith(pointlist.Entry{ConnectorID: "c1", LocalID: "l1", PointID: "p1"})

	pumpDone := make(chan struct{})
	t.Cleanup(func() { <-pumpDone; buf.Close() }) // runs after the normalizer's cleanup closes Frames
	before := metrics.NormalizerRedelivered()

	n := startNormalizer(t, src, r)
	// Let both copies queue before the Pump starts: that is the "Pump fell behind" state.
	require.Eventually(t, func() bool { return len(n.Frames()) == 2 }, 2*time.Second, 5*time.Millisecond)
	go func() { defer close(pumpDone); storeforward.Pump(context.Background(), n.Frames(), buf) }()

	require.Eventually(t, func() bool { return original.acked() && redelivered.acked() }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, int64(1), buf.Written(), "one frame per stream message")
	assert.Equal(t, int64(1), buf.Duplicates())
	assert.Equal(t, before+1, metrics.NormalizerRedelivered())
}
