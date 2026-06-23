// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

// TestE2E_DualBOSAddr verifies that the gateway can simultaneously connect
// GatewayIngressService and GatewayEgressService to two different BOS addresses
// (e.g. port 5051 for telemetry, port 5052 for control) — the split introduced
// when BOS distributes the two services across separate gRPC servers.
//
// This test wires uplink.Ingress → ingressAddr and egress.Agent → egressAddr
// independently, publishes a Common Event, then sends a ControlCommand, and
// confirms both paths work concurrently with no cross-wiring.

import (
	"context"
	"encoding/json"
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
	"nexus-gateway/internal/dispatch"
	"nexus-gateway/internal/egress"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/storeforward"
	"nexus-gateway/internal/uplink"
)

func TestE2E_DualBOSAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── NATS ─────────────────────────────────────────────────────────────────
	ns := startEmbeddedNATS(t)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "EVENTS",
		Subjects: []string{"evt.>"},
		Storage:  jetstream.MemoryStorage,
	})
	require.NoError(t, err)

	// ── Point list ────────────────────────────────────────────────────────────
	resolver := pointlist.NewSynced(nil)
	resolver.Update([]pointlist.Entry{
		{ConnectorID: "sim-01", Protocol: "sim", LocalID: "sim://ahu-01/supply_air_temp", PointID: "supply_air_temp", Unit: "Cel", DeviceRef: "sim://ahu-01"},
		{ConnectorID: "bacnet-01", Protocol: "bacnet", LocalID: "L-023", PointID: "SOS-PT-023", Writable: true, DeviceRef: "SOS-DEV-009"},
	})

	// ── Mock GatewayIngressService on its own port ────────────────────────────
	ingressReceived := make(chan *pb.TelemetryFrame, 4)
	var ingressAccepted atomic.Int64
	ingressHandle := startMockBOS(t, ingressReceived, &ingressAccepted)

	// ── Mock GatewayEgressService on a separate port ──────────────────────────
	egressSrv := &dualTestEgressServer{
		results: make(chan *pb.ControlResult, 4),
		downMsgs: []*pb.EgressDown{
			{M: &pb.EgressDown_Command{Command: &pb.ControlCommand{
				ControlId: "ctrl-dual-1", PointId: "SOS-PT-023", PresentValue: 21.5,
			}}},
		},
	}
	egressAddr := startDualMockEgress(t, egressSrv)

	// Ingress addr and egress addr must differ (the whole point of this test).
	require.NotEqual(t, ingressHandle.addr, egressAddr, "test setup: ingress and egress must be on different addresses")

	// ── Store-and-Forward ─────────────────────────────────────────────────────
	buf, err := storeforward.Open(t.TempDir()+"/sf.db", 1000)
	require.NoError(t, err)
	t.Cleanup(func() { _ = buf.Close() })

	// ── Normalizer ────────────────────────────────────────────────────────────
	norm, err := normalizer.New(ctx, js, resolver, "gw-dual")
	require.NoError(t, err)
	go storeforward.Pump(ctx, norm.Frames(), buf)

	// ── Ingress uplink → ingressHandle.addr ──────────────────────────────────
	ul, err := uplink.NewIngress(ctx, ingressHandle.addr, "gw-dual", buf,
		uplink.Config{CheckpointSize: 100, CheckpointAge: 100 * time.Millisecond},
		insecureCreds())
	require.NoError(t, err)
	go ul.Run(ctx)

	// ── Egress agent → egressAddr (different port) ────────────────────────────
	d := dispatch.New(nc, resolver, 5*time.Second)
	// Subscribe a mock connector on NATS so dispatch can complete.
	sub, err := nc.Subscribe("cmd.bacnet.bacnet-01", func(msg *nats.Msg) {
		reply := dispatch.ConnectorReply{Success: true, Response: "ok"}
		data, _ := json.Marshal(reply)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	go egress.New(egressAddr, "gw-dual", d, insecureCreds(), nil).Run(ctx)

	// ── Publish a Common Event to exercise the ingress path ───────────────────
	evt := common.Event{
		Protocol: "sim", ConnectorID: "sim-01",
		LocalID: "sim://ahu-01/supply_air_temp", DeviceRef: "sim://ahu-01",
		Value: 23.4, Unit: "Cel", Quality: "Good",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)
	_, err = js.Publish(ctx, "evt.sim.sim-01", payload)
	require.NoError(t, err)

	// ── Assert: telemetry arrives at the ingress server ───────────────────────
	t.Run("telemetry arrives at ingress addr", func(t *testing.T) {
		select {
		case frame := <-ingressReceived:
			assert.Equal(t, "gw-dual", frame.GatewayId)
			assert.Equal(t, "supply_air_temp", frame.PointId)
			assert.InDelta(t, 23.4, frame.Value, 0.0001)
			assert.GreaterOrEqual(t, ingressAccepted.Load(), int64(1), "mock ingress must have accepted at least one frame")
		case <-ctx.Done():
			t.Fatal("timeout: no TelemetryFrame at ingress addr")
		}
	})

	// ── Assert: control command dispatched via egress server ──────────────────
	t.Run("control command dispatched via egress addr", func(t *testing.T) {
		select {
		case r := <-egressSrv.results:
			assert.Equal(t, "ctrl-dual-1", r.ControlId)
			assert.True(t, r.Success)
		case <-ctx.Done():
			t.Fatal("timeout: no ControlResult from egress addr")
		}
	})
}

// ── dual-addr test helpers ────────────────────────────────────────────────────

type dualTestEgressServer struct {
	pb.UnimplementedGatewayEgressServer
	downMsgs []*pb.EgressDown
	results  chan *pb.ControlResult
}

func (s *dualTestEgressServer) Connect(stream pb.GatewayEgress_ConnectServer) error {
	if _, err := stream.Recv(); err != nil { // consume Hello
		return err
	}
	for _, msg := range s.downMsgs {
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	for {
		up, err := stream.Recv()
		if err != nil {
			return nil
		}
		if r := up.GetResult(); r != nil {
			s.results <- r
		}
	}
}

func startDualMockEgress(t *testing.T, srv *dualTestEgressServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	pb.RegisterGatewayEgressServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.GracefulStop)
	return lis.Addr().String()
}
