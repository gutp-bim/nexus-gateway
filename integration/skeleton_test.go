// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
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

// TestE2E_SimConnectorFrameArrivesAtBOS is the tracer bullet: one Common Event
// published by the sim connector must arrive at the mock BOS as a TelemetryFrame
// with the resolved point_id (not the native local_id).
func TestE2E_SimConnectorFrameArrivesAtBOS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── 1. Embedded NATS with JetStream ──────────────────────────────────────
	ns := startEmbeddedNATS(t)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      "EVENTS",
		Subjects:  []string{"evt.>"},
		MaxAge:    48 * time.Hour,
		MaxBytes:  2 * 1024 * 1024 * 1024, // 2 GB
		Discard:   jetstream.DiscardOld,
		Storage:   jetstream.MemoryStorage, // fast for tests
		Retention: jetstream.LimitsPolicy,
	})
	require.NoError(t, err)

	// ── 2. Mock Building OS Ingress gRPC server ───────────────────────────────
	var acceptedFrames atomic.Int64
	received := make(chan *pb.TelemetryFrame, 10)
	mockBOS := startMockBOS(t, received, &acceptedFrames)

	// ── 3. Fixture Point List: local_id → point_id ────────────────────────────
	pl := pointlist.NewFixture([]pointlist.Entry{
		{
			ConnectorID: "sim-01",
			Protocol:    "sim",
			LocalID:     "sim://ahu-01/supply_air_temp",
			PointID:     "supply_air_temp",
		},
	})

	// ── 4. Normalizer: consumes evt.>, resolves point_id ─────────────────────
	norm, err := normalizer.New(ctx, js, pl, "gw-001")
	require.NoError(t, err)

	// ── 5. Store-and-Forward buffer + Ingress uplink ─────────────────────────
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 1000)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })
	go storeforward.Pump(ctx, norm.Frames(), buf)

	ul, err := uplink.NewIngress(ctx, mockBOS.addr, "gw-001", buf, uplink.Config{CheckpointSize: 1000, CheckpointAge: 100 * time.Millisecond}, insecureCreds())
	require.NoError(t, err)
	go ul.Run(ctx)

	// ── 6. Sim connector publishes one Common Event ───────────────────────────
	evt := common.Event{
		Protocol:    "sim",
		ConnectorID: "sim-01",
		LocalID:     "sim://ahu-01/supply_air_temp",
		DeviceRef:   "sim://ahu-01",
		Value:       23.4,
		Unit:        "Cel",
		Quality:     "Good",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)

	_, err = js.Publish(ctx, "evt.sim.sim-01", payload)
	require.NoError(t, err)

	// ── 7. Assert frame arrives at mock BOS ───────────────────────────────────
	select {
	case frame := <-received:
		assert.Equal(t, "gw-001", frame.GatewayId)
		assert.Equal(t, "supply_air_temp", frame.PointId, "Normalizer must resolve local_id → point_id")
		assert.InDelta(t, 23.4, frame.Value, 0.0001)
		assert.NotEmpty(t, frame.Timestamp)
	case <-ctx.Done():
		t.Fatal("timeout: no TelemetryFrame received at mock BOS")
	}
}

// TestE2E_NativeAddressingOnlyInEventStream verifies that common events carry
// native addressing only — point_id must NOT appear in the EVENTS stream payload.
func TestE2E_NativeAddressingOnlyInEventStream(t *testing.T) {
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

	evt := common.Event{
		Protocol:    "sim",
		ConnectorID: "sim-01",
		LocalID:     "sim://ahu-01/supply_air_temp",
		DeviceRef:   "sim://ahu-01",
		Value:       42.0,
		Unit:        "Cel",
		Quality:     "Good",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)
	_, err = js.Publish(ctx, "evt.sim.sim-01", payload)
	require.NoError(t, err)

	// Read raw message back; confirm no point_id field (ADR-0001)
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		Durable:       "test-verify",
		FilterSubject: "evt.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)

	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	require.NoError(t, err)
	for msg := range msgs.Messages() {
		var raw map[string]any
		require.NoError(t, json.Unmarshal(msg.Data(), &raw))
		assert.NotContains(t, raw, "point_id", "Connector must not emit canonical point_id (ADR-0001)")
		assert.Contains(t, raw, "local_id", "Connector must emit native local_id")
		_ = msg.Ack()
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func startEmbeddedNATS(t *testing.T) *server.Server {
	t.Helper()
	opts := &server.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		Port:      -1, // random port
	}
	ns, err := server.NewServer(opts)
	require.NoError(t, err)
	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second), "NATS did not start")
	t.Cleanup(ns.Shutdown)
	return ns
}

type mockBOSServer struct {
	pb.UnimplementedGatewayIngressServer
	received chan *pb.TelemetryFrame
	accepted *atomic.Int64
}

func (s *mockBOSServer) StreamTelemetry(stream pb.GatewayIngress_StreamTelemetryServer) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return stream.SendAndClose(&pb.StreamAck{Accepted: s.accepted.Load()})
		}
		s.received <- frame
		s.accepted.Add(1)
	}
}

type mockBOSHandle struct {
	addr string
	srv  *grpc.Server
}

func startMockBOS(t *testing.T, received chan *pb.TelemetryFrame, accepted *atomic.Int64) *mockBOSHandle {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	pb.RegisterGatewayIngressServer(srv, &mockBOSServer{received: received, accepted: accepted})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)

	return &mockBOSHandle{addr: lis.Addr().String(), srv: srv}
}
