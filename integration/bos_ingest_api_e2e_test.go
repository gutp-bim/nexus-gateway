// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "nexus-gateway/gen"
)

// TestE2E_BosIngestAPI verifies the full ingest path:
//
//	nexus-gateway gRPC StreamTelemetry → Building OS TimescaleDB → /telemetries/hot API
//
// This is the M4 (ingest) + M5 (API read-back) milestone check for the SoS deployment.
//
// Run with:
//
//	E2E_BOS_INGRESS_URL=localhost:5051 E2E_BOS_API_URL=http://localhost:5000 \
//	  go test ./integration/... -run TestE2E_BosIngestAPI -v -timeout 60s
//
// The test skips automatically when E2E_BOS_INGRESS_URL is unset (normal CI run, ADR-0004).
func TestE2E_BosIngestAPI(t *testing.T) {
	bosAddr := os.Getenv("E2E_BOS_INGRESS_URL")
	if bosAddr == "" {
		t.Skip("E2E_BOS_INGRESS_URL not set — set both E2E_BOS_INGRESS_URL and E2E_BOS_API_URL to run")
	}
	apiBase := os.Getenv("E2E_BOS_API_URL")
	if apiBase == "" {
		apiBase = "http://localhost:5000"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(bosAddr, grpc.WithTransportCredentials(insecureCreds()))
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewGatewayIngressClient(conn)

	// Use a value that is unlikely to collide with standing fixture data.
	// We verify via datetime: the API entry must be newer than T0.
	const (
		gatewayID = "GW-SOS-001"
		pointID   = "SOS-PT-001"
		sentValue = 77.77
	)
	t0 := time.Now().UTC().Truncate(time.Second)

	t.Run("M4: frame ingested by Building OS", func(t *testing.T) {
		stream, err := client.StreamTelemetry(ctx)
		require.NoError(t, err)

		err = stream.Send(&pb.TelemetryFrame{
			GatewayId: gatewayID,
			PointId:   pointID,
			Value:     sentValue,
			Timestamp: t0.Format(time.RFC3339),
		})
		require.NoError(t, err)

		ack, err := stream.CloseAndRecv()
		require.NoError(t, err)
		assert.EqualValues(t, 1, ack.Accepted, "frame must be accepted (M4)")
	})

	t.Run("M5: accepted frame readable via /telemetries/hot", func(t *testing.T) {
		url := fmt.Sprintf("%s/telemetries/hot?pointId=%s", apiBase, pointID)

		require.Eventually(t, func() bool {
			resp, err := http.Get(url) //nolint:noctx
			if err != nil || resp.StatusCode != http.StatusOK {
				return false
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			var result struct {
				PointID  string  `json:"pointId"`
				Value    float64 `json:"value"`
				Datetime string  `json:"datetime"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				return false
			}
			ts, err := time.Parse(time.RFC3339, result.Datetime)
			if err != nil {
				// Try the extended RFC3339 format Building OS returns
				ts, err = time.Parse("2006-01-02T15:04:05.9999999Z07:00", result.Datetime)
				if err != nil {
					return false
				}
			}
			// Accept when the API reflects our write (datetime ≥ T0 and value matches).
			return result.PointID == pointID &&
				result.Value == sentValue &&
				!ts.Before(t0)
		}, 45*time.Second, 2*time.Second, "M5: /telemetries/hot must reflect the ingested value within 45s")
	})
}
