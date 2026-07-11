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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/common"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
)

// fakeMsg records which ack control the consume loop invoked.
type fakeMsg struct {
	data []byte
	mu   sync.Mutex
	ack  bool
	term bool
	nak  bool
}

func (m *fakeMsg) Data() []byte { return m.data }
func (m *fakeMsg) Ack() error   { m.mu.Lock(); m.ack = true; m.mu.Unlock(); return nil }
func (m *fakeMsg) Term() error  { m.mu.Lock(); m.term = true; m.mu.Unlock(); return nil }
func (m *fakeMsg) Nak() error   { m.mu.Lock(); m.nak = true; m.mu.Unlock(); return nil }
func (m *fakeMsg) NakWithDelay(_ time.Duration) error {
	m.mu.Lock()
	m.nak = true
	m.mu.Unlock()
	return nil
}
func (m *fakeMsg) acked() bool  { m.mu.Lock(); defer m.mu.Unlock(); return m.ack }
func (m *fakeMsg) termed() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.term }

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
	b, err := json.Marshal(common.Event{ConnectorID: connectorID, LocalID: localID, Value: value, Timestamp: "2026-01-01T00:00:00Z"})
	require.NoError(t, err)
	return b
}

func resolverWith(entries ...pointlist.Entry) pointlist.Resolver {
	return pointlist.NewFixture(entries)
}

// A resolved Common Event becomes a TelemetryFrame on Frames() that carries its
// source message, and is NOT acked on enqueue — durability requires the Pump to
// ack only after a successful buffer write (#28).
func TestNormalizer_OKEmitsFrameCarryingMsgWithoutAcking(t *testing.T) {
	msg := &fakeMsg{data: eventJSON(t, "c1", "l1", 1.5)}
	src := &fakeSource{batches: [][]normalizer.EventMsg{{msg}}}
	r := resolverWith(pointlist.Entry{ConnectorID: "c1", LocalID: "l1", PointID: "p1"})

	n := startNormalizer(t, src, r)

	select {
	case fm := <-n.Frames():
		assert.Equal(t, "p1", fm.Frame.PointId)
		assert.Equal(t, "gw-1", fm.Frame.GatewayId)
		assert.Equal(t, 1.5, fm.Frame.Value)
		assert.True(t, fm.Msg == msg, "frame must carry its source msg so the Pump can ack after the durable write")
	case <-time.After(2 * time.Second):
		t.Fatal("expected a TelemetryFrame")
	}
	// Give the consume loop time to (wrongly) ack, then assert it did not.
	time.Sleep(50 * time.Millisecond)
	assert.False(t, msg.acked(), "normalizer must not ack before the durable write")
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
