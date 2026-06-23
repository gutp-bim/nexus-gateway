// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/common"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/storeforward"
	"nexus-gateway/internal/uplink"
)

// TestSF_OutageSurvival verifies that frames produced during a BOS outage
// are buffered in SQLite and forwarded in order once the BOS recovers.
func TestSF_OutageSurvival(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Embedded NATS
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

	// Fixture point list
	pl := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l1", PointID: "p1"},
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l2", PointID: "p2"},
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "l3", PointID: "p3"},
	})

	norm, err := normalizer.New(ctx, js, pl, "gw-001")
	require.NoError(t, err)

	// SQLite buffer (capacity 1000)
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 1000)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })

	// Pump normalizer → buffer
	go storeforward.Pump(ctx, norm.Frames(), buf)

	// Start mock BOS — initially up
	received := make(chan *pb.TelemetryFrame, 100)
	var accepted atomic.Int64
	bos := startMockBOS(t, received, &accepted)

	cfg := uplink.Config{CheckpointSize: 1000, CheckpointAge: 100 * time.Millisecond}
	ul, err := uplink.NewIngress(ctx, bos.addr, "gw-001", buf, cfg, insecureCreds())
	require.NoError(t, err)
	go ul.Run(ctx)

	// Publish 3 frames while BOS is up — they must arrive AND be checkpointed
	for _, lid := range []string{"l1", "l2", "l3"} {
		publish(t, js, "sim-01", lid, 1.0)
	}
	for i := range 3 {
		select {
		case <-received:
		case <-ctx.Done():
			t.Fatalf("timeout waiting for frame %d before outage", i)
		}
	}
	// Wait for cursor to advance: confirms checkpoint completed and cursor persisted
	require.Eventually(t, func() bool { return buf.Cursor() > 0 }, 5*time.Second, 20*time.Millisecond,
		"checkpoint must complete before simulating outage")

	// Simulate BOS outage: stop accepting connections
	bos.srv.Stop()
	slog.Info("mock BOS stopped")

	// Publish 3 more frames during outage — they land in SQLite
	for _, lid := range []string{"l1", "l2", "l3"} {
		publish(t, js, "sim-01", lid, 2.0)
	}
	// Wait for outage frames to land in SQLite buffer before restarting BOS
	preOutageCursor := buf.Cursor()
	require.Eventually(t, func() bool {
		batch, _ := buf.ReadBatch(preOutageCursor, 10)
		return len(batch) >= 3
	}, 5*time.Second, 20*time.Millisecond, "outage frames must be in buffer before BOS restart")

	// Restart mock BOS on same port
	restartMockBOS(t, bos.addr, received, &accepted)
	slog.Info("mock BOS restarted", "addr", bos.addr)

	// Expect all 3 buffered frames to arrive, in order
	var vals []float64
	for i := range 3 {
		select {
		case f := <-received:
			vals = append(vals, f.Value)
		case <-ctx.Done():
			t.Fatalf("timeout waiting for buffered frame %d after restart", i)
		}
	}
	assert.Equal(t, []float64{2.0, 2.0, 2.0}, vals, "buffered frames must arrive after BOS recovery")
}

// TestSF_ImmediateSend confirms healthy-state latency is sub-second even with a 5s checkpoint.
func TestSF_ImmediateSend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	pl := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})
	norm, err := normalizer.New(ctx, js, pl, "gw-001")
	require.NoError(t, err)

	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })
	go storeforward.Pump(ctx, norm.Frames(), buf)

	received := make(chan *pb.TelemetryFrame, 10)
	var acc atomic.Int64
	bos := startMockBOS(t, received, &acc)

	cfg := uplink.Config{CheckpointSize: 1000, CheckpointAge: 5 * time.Second} // long checkpoint
	ul, err := uplink.NewIngress(ctx, bos.addr, "gw-001", buf, cfg, insecureCreds())
	require.NoError(t, err)
	go ul.Run(ctx)

	start := time.Now()
	publish(t, js, "c1", "l1", 42.0)

	select {
	case f := <-received:
		latency := time.Since(start)
		assert.Less(t, latency, time.Second, "frame must arrive in <1s despite 5s checkpoint")
		assert.Equal(t, "p1", f.PointId)
	case <-ctx.Done():
		t.Fatal("timeout: frame never arrived")
	}
}

// TestSF_DriftCounterRises verifies drift counter increments when BOS accepts < sent.
func TestSF_DriftCounterRises(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	pl := pointlist.NewFixture([]pointlist.Entry{
		{ConnectorID: "c1", Protocol: "sim", LocalID: "l1", PointID: "p1"},
	})
	norm, err := normalizer.New(ctx, js, pl, "gw-001")
	require.NoError(t, err)

	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })
	go storeforward.Pump(ctx, norm.Frames(), buf)

	// Mock BOS that reports accepted=0 (rejects everything)
	received2 := make(chan *pb.TelemetryFrame, 10)
	bos := startMockBOSWithAccepted(t, received2)

	cfg := uplink.Config{CheckpointSize: 2, CheckpointAge: 5 * time.Second}
	ul, err := uplink.NewIngress(ctx, bos.addr, "gw-001", buf, cfg, insecureCreds())
	require.NoError(t, err)
	go ul.Run(ctx)

	// Publish 2 frames — triggers checkpoint (size=2)
	publish(t, js, "c1", "l1", 1.0)
	publish(t, js, "c1", "l1", 2.0)

	// Wait for checkpoint to fire
	require.Eventually(t, func() bool {
		drifts := buf.Drifts()
		return drifts["p1"] > 0
	}, 5*time.Second, 50*time.Millisecond, "drift counter for p1 should be > 0")
}

// ── Test helpers ─────────────────────────────────────────────────────────────

func publish(t *testing.T, js jetstream.JetStream, connectorID, localID string, val float64) {
	t.Helper()
	ctx := context.Background()
	evt := common.Event{
		Protocol: "sim", ConnectorID: connectorID, LocalID: localID,
		DeviceRef: "dev", Value: val, Unit: "Cel", Quality: "Good",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)
	_, err = js.Publish(ctx, "evt.sim."+connectorID, payload)
	require.NoError(t, err)
}

func restartMockBOS(t *testing.T, addr string, received chan *pb.TelemetryFrame, accepted *atomic.Int64) *mockBOSHandle {
	t.Helper()
	lis, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	srv := grpc.NewServer()
	pb.RegisterGatewayIngressServer(srv, &mockBOSServer{received: received, accepted: accepted})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	return &mockBOSHandle{addr: addr, srv: srv}
}

// mockBOSServer that reports accepted=0 (simulates full rejection for drift test)
type rejectBOSServer struct {
	pb.UnimplementedGatewayIngressServer
	received chan *pb.TelemetryFrame
}

func (s *rejectBOSServer) StreamTelemetry(stream pb.GatewayIngress_StreamTelemetryServer) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return stream.SendAndClose(&pb.StreamAck{Accepted: 0}) // always reject
		}
		s.received <- frame
	}
}

func startMockBOSWithAccepted(t *testing.T, received chan *pb.TelemetryFrame) *mockBOSHandle {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	pb.RegisterGatewayIngressServer(srv, &rejectBOSServer{received: received})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	return &mockBOSHandle{addr: lis.Addr().String(), srv: srv}
}
