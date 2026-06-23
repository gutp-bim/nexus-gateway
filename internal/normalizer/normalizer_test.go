// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package normalizer_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/common"
	"nexus-gateway/internal/metrics"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
)

func TestNormalizer_ResolvesLocalIDToPointID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js := newTestJetStream(t, ctx)
	pl := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "conn-01", Protocol: "sim", LocalID: "dev/temp", PointID: "zone_a/temp"},
	})

	norm, err := normalizer.New(ctx, js, pl, "gw-test")
	require.NoError(t, err)

	publish(t, ctx, js, "evt.sim.conn-01", common.Event{
		ConnectorID: "conn-01", Protocol: "sim", LocalID: "dev/temp",
		Value: 21.5, Unit: "Cel", Quality: "Good",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	select {
	case frame := <-norm.Frames():
		assert.Equal(t, "gw-test", frame.GatewayId)
		assert.Equal(t, "zone_a/temp", frame.PointId)
		assert.InDelta(t, 21.5, frame.Value, 0.001)
	case <-ctx.Done():
		t.Fatal("timeout waiting for frame")
	}
}

func TestNormalizer_SkipsUnknownLocalID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js := newTestJetStream(t, ctx)
	pl := pointlist.NewFixture(nil) // empty — nothing resolves

	norm, err := normalizer.New(ctx, js, pl, "gw-test")
	require.NoError(t, err)

	publish(t, ctx, js, "evt.sim.conn-01", common.Event{
		ConnectorID: "conn-01", LocalID: "unknown/point",
		Value: 1.0, Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	// Also publish a known event after so we can detect order
	select {
	case <-norm.Frames():
		t.Fatal("frame must not be emitted for unknown local_id")
	case <-time.After(500 * time.Millisecond):
		// expected: no frame
	}
}

func TestNormalizer_BoolValueNormalisedToNumeric(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js := newTestJetStream(t, ctx)
	pl := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c", Protocol: "sim", LocalID: "fan/run", PointID: "fan_run"},
	})
	norm, err := normalizer.New(ctx, js, pl, "gw-test")
	require.NoError(t, err)

	// value=1 represents bool true (bool→0/1, CONTEXT.md)
	publish(t, ctx, js, "evt.sim.c", common.Event{
		ConnectorID: "c", LocalID: "fan/run",
		Value: 1.0, Quality: "Good",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	select {
	case frame := <-norm.Frames():
		assert.Equal(t, 1.0, frame.Value, "bool true must arrive as 1.0")
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}

func TestNormalizer_DropsAndMetersPoisonAndMiss(t *testing.T) {
	// NOTE: reads the process-global metrics counters via before/after deltas, so
	// this test must NOT be t.Parallel()'d and no other test in this package that
	// produces a miss/poison may run concurrently — that would perturb the delta.
	// (Package tests are serial by default; this comment is the guard against a
	// future t.Parallel() silently introducing flakiness.)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js := newTestJetStream(t, ctx)
	pl := pointlist.NewFixture(nil) // empty — every event is a miss

	invBefore := metrics.NormalizerInvalid()
	unrBefore := metrics.NormalizerUnresolved()

	norm, err := normalizer.New(ctx, js, pl, "gw-test")
	require.NoError(t, err)

	// Poison: raw bytes that are not a valid Common Event.
	_, err = js.Publish(ctx, "evt.sim.c", []byte("{not valid json"))
	require.NoError(t, err)

	// Miss: well-formed event whose local_id resolves to nothing.
	publish(t, ctx, js, "evt.sim.c", common.Event{
		ConnectorID: "c", LocalID: "unknown/point",
		Value: 1.0, Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	// Each is metered exactly once.
	require.Eventually(t, func() bool {
		return metrics.NormalizerInvalid()-invBefore == 1 &&
			metrics.NormalizerUnresolved()-unrBefore == 1
	}, 3*time.Second, 20*time.Millisecond, "poison and miss should each be metered once")

	// No frame is emitted for either.
	select {
	case <-norm.Frames():
		t.Fatal("no frame expected for poison/miss")
	case <-time.After(300 * time.Millisecond):
	}

	// Counters stay at 1: the messages were Term()-ed, not Nak()-redelivered (which
	// would climb toward MaxDeliver under the old behavior).
	assert.Equal(t, int64(1), metrics.NormalizerInvalid()-invBefore)
	assert.Equal(t, int64(1), metrics.NormalizerUnresolved()-unrBefore)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestJetStream(t *testing.T, ctx context.Context) jetstream.JetStream {
	t.Helper()
	opts := &server.Options{JetStream: true, StoreDir: t.TempDir(), Port: -1}
	ns, err := server.NewServer(opts)
	require.NoError(t, err)
	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second))
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "EVENTS", Subjects: []string{"evt.>"},
		Storage: jetstream.MemoryStorage,
	})
	require.NoError(t, err)
	return js
}

func publish(t *testing.T, ctx context.Context, js jetstream.JetStream, subject string, evt common.Event) {
	t.Helper()
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	_, err = js.Publish(ctx, subject, data)
	require.NoError(t, err)
}
