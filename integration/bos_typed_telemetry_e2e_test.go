// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "nexus-gateway/gen"
)

// TestE2E_BosTypedTelemetry proves the Building OS-owned oneof contract through
// the live ingress, hot store, and REST read path for every supported scalar.
func TestE2E_BosTypedTelemetry(t *testing.T) {
	bosAddr := os.Getenv("E2E_BOS_INGRESS_URL")
	if bosAddr == "" {
		t.Skip("E2E_BOS_INGRESS_URL not set — start Building OS to run")
	}
	apiBase := os.Getenv("E2E_BOS_API_URL")
	if apiBase == "" {
		apiBase = "http://localhost:5000"
	}
	apiToken := os.Getenv("E2E_BOS_API_TOKEN")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(bosAddr, grpc.WithTransportCredentials(insecureCreds()))
	require.NoError(t, err)
	defer conn.Close()

	timestamp := time.Now().UTC().Truncate(time.Second)
	cases := []struct {
		name, pointID string
		frame         *pb.TelemetryFrame
		assert        func(*testing.T, hotTelemetry)
	}{
		{"number", "SOS-PT-001", &pb.TelemetryFrame{Value: &pb.TelemetryFrame_ValueNum{ValueNum: 64.125}}, func(t *testing.T, got hotTelemetry) {
			require.True(t, got.ValueType == "" || got.ValueType == "number")
			require.NotNil(t, got.Value)
			require.Equal(t, 64.125, *got.Value)
			require.Nil(t, got.ValueText)
			require.Nil(t, got.ValueBool)
		}},
		{"string", "SOS-PT-002", &pb.TelemetryFrame{Value: &pb.TelemetryFrame_ValueStr{ValueStr: "occupied"}}, func(t *testing.T, got hotTelemetry) {
			require.Equal(t, "string", got.ValueType)
			require.NotNil(t, got.ValueText)
			require.Equal(t, "occupied", *got.ValueText)
			require.Nil(t, got.Value)
			require.Nil(t, got.ValueBool)
		}},
		{"boolean", "SOS-PT-003", &pb.TelemetryFrame{Value: &pb.TelemetryFrame_ValueBool{ValueBool: false}}, func(t *testing.T, got hotTelemetry) {
			require.Equal(t, "boolean", got.ValueType)
			require.NotNil(t, got.ValueBool, "false must retain oneof presence")
			require.False(t, *got.ValueBool)
			require.Nil(t, got.Value)
			require.Nil(t, got.ValueText)
		}},
	}

	stream, err := pb.NewGatewayIngressClient(conn).StreamTelemetry(ctx)
	require.NoError(t, err)
	for _, tc := range cases {
		tc.frame.GatewayId = "GW-SOS-001"
		tc.frame.PointId = tc.pointID
		tc.frame.Timestamp = timestamp.Format(time.RFC3339)
		require.NoError(t, stream.Send(tc.frame))
	}
	ack, err := stream.CloseAndRecv()
	require.NoError(t, err)
	require.EqualValues(t, len(cases), ack.Accepted)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got hotTelemetry
			require.Eventually(t, func() bool {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/telemetries/hot?pointId=%s", apiBase, tc.pointID), nil)
				if err != nil {
					return false
				}
				if apiToken != "" {
					req.Header.Set("Authorization", "Bearer "+apiToken)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil || resp.StatusCode != http.StatusOK {
					if resp != nil {
						resp.Body.Close()
					}
					return false
				}
				defer resp.Body.Close()
				if json.NewDecoder(resp.Body).Decode(&got) != nil || got.PointID != tc.pointID {
					return false
				}
				observed, err := time.Parse(time.RFC3339Nano, got.Datetime)
				return err == nil && !observed.Before(timestamp)
			}, 60*time.Second, time.Second)
			tc.assert(t, got)
		})
	}
}

type hotTelemetry struct {
	PointID   string   `json:"pointId"`
	Datetime  string   `json:"datetime"`
	Value     *float64 `json:"value"`
	ValueType string   `json:"valueType"`
	ValueText *string  `json:"valueText"`
	ValueBool *bool    `json:"valueBool"`
}
