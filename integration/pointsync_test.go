// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/pointsync"
	"nexus-gateway/internal/provisioning"
	"nexus-gateway/internal/storeforward"
	"nexus-gateway/internal/uplink"
)

// TestPointSync_LiveRemap verifies that changing a local_id → point_id mapping in the
// mock provisioning API causes subsequent frames to arrive with the new point_id,
// without any process restart.
func TestPointSync_LiveRemap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ns := startEmbeddedNATS(t)
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

	// Mock provisioning API
	mockAPI := provisioning.NewMock([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "temp", PointID: "zone_temp_v1"},
	})

	// SyncedResolver + sync loop
	resolver := pointlist.NewSynced(nil)
	loop := pointsync.New(mockAPI, resolver, pointsync.Config{Interval: 20 * time.Millisecond})
	go loop.Run(ctx)

	require.Eventually(t, func() bool {
		_, ok := resolver.Resolve("c1", "temp")
		return ok
	}, 2*time.Second, 10*time.Millisecond, "initial mapping must load")

	// Normalizer + S&F + Ingress
	norm, err := normalizer.New(ctx, js, resolver, "gw-001")
	require.NoError(t, err)
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 1000)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })
	go storeforward.Pump(ctx, norm.Frames(), buf)

	received := make(chan *pb.TelemetryFrame, 20)
	var acc atomic.Int64
	bos := startMockBOS(t, received, &acc)
	ul, err := uplink.NewIngress(ctx, bos.addr, "gw-001", buf,
		uplink.Config{CheckpointSize: 1000, CheckpointAge: 100 * time.Millisecond}, insecureCreds())
	require.NoError(t, err)
	go ul.Run(ctx)

	// Phase 1: original mapping
	publish(t, js, "c1", "temp", 20.0)
	select {
	case f := <-received:
		assert.Equal(t, "zone_temp_v1", f.PointId)
	case <-ctx.Done():
		t.Fatal("timeout waiting for frame with v1 mapping")
	}

	// Phase 2: update provisioning API
	mockAPI.SetSnapshot([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "temp", PointID: "zone_temp_v2"},
	})
	require.Eventually(t, func() bool {
		pid, _ := resolver.Resolve("c1", "temp")
		return pid == "zone_temp_v2"
	}, 2*time.Second, 10*time.Millisecond, "resolver must reconverge to v2")

	// Phase 3: new events carry the updated point_id
	publish(t, js, "c1", "temp", 21.0)
	for {
		select {
		case f := <-received:
			if f.PointId == "zone_temp_v2" {
				assert.Equal(t, "zone_temp_v2", f.PointId)
				return
			}
		case <-ctx.Done():
			t.Fatal("timeout waiting for frame with v2 mapping")
		}
	}
}

// TestPointSync_RemovalSkipsFrames verifies that removing a point stops its normalization.
func TestPointSync_RemovalSkipsFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ns := startEmbeddedNATS(t)
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

	mockAPI := provisioning.NewMock([]pointlist.Entry{
		{ConnectorID: "c2", Protocol: "sim", LocalID: "hum", PointID: "humidity"},
	})
	resolver := pointlist.NewSynced(nil)
	loop := pointsync.New(mockAPI, resolver, pointsync.Config{Interval: 20 * time.Millisecond})
	go loop.Run(ctx)

	require.Eventually(t, func() bool {
		_, ok := resolver.Resolve("c2", "hum")
		return ok
	}, 2*time.Second, 10*time.Millisecond)

	norm, err := normalizer.New(ctx, js, resolver, "gw-001")
	require.NoError(t, err)
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })
	go storeforward.Pump(ctx, norm.Frames(), buf)

	received := make(chan *pb.TelemetryFrame, 10)
	var acc atomic.Int64
	bos := startMockBOS(t, received, &acc)
	ul, err := uplink.NewIngress(ctx, bos.addr, "gw-001", buf,
		uplink.Config{CheckpointSize: 1000, CheckpointAge: 100 * time.Millisecond}, insecureCreds())
	require.NoError(t, err)
	go ul.Run(ctx)

	// Confirm frame arrives before removal
	publish(t, js, "c2", "hum", 55.0)
	select {
	case <-received:
	case <-ctx.Done():
		t.Fatal("timeout before removal")
	}

	// Remove the point
	mockAPI.SetSnapshot([]pointlist.Entry{})
	require.Eventually(t, func() bool {
		_, ok := resolver.Resolve("c2", "hum")
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "point must be removed from resolver")

	// Publish another frame — must be dropped by normalizer
	publish(t, js, "c2", "hum", 56.0)
	select {
	case f := <-received:
		t.Errorf("unexpected frame for removed point: %s", f.PointId)
	case <-time.After(600 * time.Millisecond):
		// expected: frame dropped
	}
}
