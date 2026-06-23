// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "nexus-gateway/gen"
)

// TestE2E_BosIngress verifies that the live Building OS GatewayIngressService
// accepts frames with a known (gateway_id, point_id) pair and rejects frames
// whose point_id is not in the provisioned point list.
//
// Prerequisites (not part of normal CI — ADR-0004):
//
//	docker compose up -d   # or the building-os profile that exposes :5051
//
// Run with:
//
//	E2E_BOS_INGRESS_URL=localhost:5051 go test ./integration/... -run TestE2E_BosIngress -v -timeout 60s
//
// The test skips automatically when E2E_BOS_INGRESS_URL is unset.
func TestE2E_BosIngress(t *testing.T) {
	bosAddr := os.Getenv("E2E_BOS_INGRESS_URL")
	if bosAddr == "" {
		t.Skip("E2E_BOS_INGRESS_URL not set — start Building OS and set E2E_BOS_INGRESS_URL to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(bosAddr, grpc.WithTransportCredentials(insecureCreds()))
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewGatewayIngressClient(conn)

	t.Run("known point accepted", func(t *testing.T) {
		stream, err := client.StreamTelemetry(ctx)
		require.NoError(t, err)

		err = stream.Send(&pb.TelemetryFrame{
			GatewayId: "GW-SOS-001",
			PointId:   "SOS-PT-001",
			Value:     22.5,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		require.NoError(t, err)

		ack, err := stream.CloseAndRecv()
		require.NoError(t, err)
		assert.EqualValues(t, 1, ack.Accepted, "frame with valid point_id must be accepted")
	})

	t.Run("unknown point rejected", func(t *testing.T) {
		stream, err := client.StreamTelemetry(ctx)
		require.NoError(t, err)

		err = stream.Send(&pb.TelemetryFrame{
			GatewayId: "GW-SOS-001",
			PointId:   "SOS-PT-UNKNOWN",
			Value:     0.0,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		require.NoError(t, err)

		ack, err := stream.CloseAndRecv()
		require.NoError(t, err)
		assert.EqualValues(t, 0, ack.Accepted, "frame with unknown point_id must not be accepted")
	})

	t.Run("wrong gateway_id rejected", func(t *testing.T) {
		stream, err := client.StreamTelemetry(ctx)
		require.NoError(t, err)

		err = stream.Send(&pb.TelemetryFrame{
			GatewayId: "GW-WRONG",
			PointId:   "SOS-PT-001",
			Value:     1.0,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		require.NoError(t, err)

		ack, err := stream.CloseAndRecv()
		require.NoError(t, err)
		assert.EqualValues(t, 0, ack.Accepted, "frame from wrong gateway_id must not be accepted")
	})

	t.Run("multiple known points cumulative accepted", func(t *testing.T) {
		stream, err := client.StreamTelemetry(ctx)
		require.NoError(t, err)

		for _, pid := range []string{"SOS-PT-001", "SOS-PT-002", "SOS-PT-003"} {
			err = stream.Send(&pb.TelemetryFrame{
				GatewayId: "GW-SOS-001",
				PointId:   pid,
				Value:     20.0,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			require.NoError(t, err)
		}

		ack, err := stream.CloseAndRecv()
		require.NoError(t, err)
		assert.EqualValues(t, 3, ack.Accepted, "all three known frames must be accepted cumulatively")
	})
}
