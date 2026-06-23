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

// sosPublishablePoints are the 10 sensor points in the SoS MVP point list
// (mvp-pointlist.csv, interval present → publishable). All are read-only.
var sosPublishablePoints = []struct {
	PointID string
	Value   float64
}{
	{"SOS-PT-001", 22.5},  // Entrance Temperature (C)
	{"SOS-PT-002", 55.0},  // Entrance Humidity (%)
	{"SOS-PT-003", 23.1},  // Meeting Room 101 Temperature (C)
	{"SOS-PT-004", 48.3},  // Meeting Room 101 Humidity (%)
	{"SOS-PT-005", 412.0}, // Meeting Room 101 CO2 (ppm)
	{"SOS-PT-006", 320.0}, // Meeting Room 101 Illuminance (lx)
	{"SOS-PT-007", 21.8},  // Office 102 Temperature (C)
	{"SOS-PT-008", 52.1},  // Office 102 Humidity (%)
	{"SOS-PT-009", 398.0}, // Office 102 CO2 (ppm)
	{"SOS-PT-010", 280.0}, // Office 102 Illuminance (lx)
}

// TestE2E_BosReporting verifies the SoS reporting integration (#46):
// all 10 publishable SoS sensor points can be ingested in a single
// StreamTelemetry stream and read back via the /telemetries/hot API.
//
// Run with:
//
//	E2E_BOS_INGRESS_URL=localhost:5051 E2E_BOS_API_URL=http://localhost:5000 \
//	  go test ./integration/... -run TestE2E_BosReporting -v -timeout 90s
//
// The test skips automatically when E2E_BOS_INGRESS_URL is unset (normal CI, ADR-0004).
func TestE2E_BosReporting(t *testing.T) {
	bosAddr := os.Getenv("E2E_BOS_INGRESS_URL")
	if bosAddr == "" {
		t.Skip("E2E_BOS_INGRESS_URL not set — set E2E_BOS_INGRESS_URL and E2E_BOS_API_URL to run")
	}
	apiBase := os.Getenv("E2E_BOS_API_URL")
	if apiBase == "" {
		apiBase = "http://localhost:5000"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(bosAddr, grpc.WithTransportCredentials(insecureCreds()))
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewGatewayIngressClient(conn)
	t0 := time.Now().UTC().Truncate(time.Second)

	t.Run("ingest: all 10 publishable points accepted in one stream", func(t *testing.T) {
		stream, err := client.StreamTelemetry(ctx)
		require.NoError(t, err)

		for _, pt := range sosPublishablePoints {
			err = stream.Send(&pb.TelemetryFrame{
				GatewayId: "GW-SOS-001",
				PointId:   pt.PointID,
				Value:     pt.Value,
				Timestamp: t0.Format(time.RFC3339),
			})
			require.NoError(t, err)
		}

		ack, err := stream.CloseAndRecv()
		require.NoError(t, err)
		assert.EqualValues(t, len(sosPublishablePoints), ack.Accepted,
			"all %d publishable frames must be accepted", len(sosPublishablePoints))
	})

	t.Run("reporting: /telemetries/hot reflects ingested values for all points", func(t *testing.T) {
		for _, pt := range sosPublishablePoints {
			pt := pt // capture
			t.Run(pt.PointID, func(t *testing.T) {
				t.Parallel()
				url := fmt.Sprintf("%s/telemetries/hot?pointId=%s", apiBase, pt.PointID)

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
					ts, err := time.Parse("2006-01-02T15:04:05.9999999Z07:00", result.Datetime)
					if err != nil {
						ts, err = time.Parse(time.RFC3339, result.Datetime)
						if err != nil {
							return false
						}
					}
					return result.PointID == pt.PointID &&
						result.Value == pt.Value &&
						!ts.Before(t0)
				}, 60*time.Second, 2*time.Second,
					"%s: /telemetries/hot must reflect value=%.1f within 60s", pt.PointID, pt.Value)
			})
		}
	})
}
