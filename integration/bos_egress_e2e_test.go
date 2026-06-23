// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/dispatch"
	"nexus-gateway/internal/egress"
	"nexus-gateway/internal/pointlist"
)

// egressEnv holds BOS-deployment-specific identifiers for egress E2E tests.
// Override via env vars to target any BOS instance (not just the SOS fixture).
//
//	E2E_GATEWAY_ID          — gateway identity the egress.Agent registers as (default: GW-SOS-001)
//	E2E_WRITABLE_POINT_ID   — a writable point that BOS will route control commands for (default: SOS-PT-023)
//	E2E_READONLY_POINT_ID   — a read-only point BOS must reject (default: SOS-PT-001)
//	E2E_WRITABLE_LOCAL_ID   — the connector-local ID for the writable point (default: L-023)
//	E2E_WRITABLE_CONNECTOR  — connector ID for the writable point (default: bacnet-01)
//	E2E_WRITABLE_DEVICE_REF — device ref for the writable point (default: SOS-DEV-009)
//
// Example for the SoS BOS (defaults):
//
//	(no overrides needed)
//
// Example for the nexus-gateway dev BOS (gw-001, PT006 is writable):
//
//	E2E_GATEWAY_ID=gw-001 E2E_WRITABLE_POINT_ID=PT006 E2E_READONLY_POINT_ID=PT001 \
//	E2E_WRITABLE_LOCAL_ID=analogValue,1002 E2E_WRITABLE_CONNECTOR=bacnet-01 \
//	E2E_WRITABLE_DEVICE_REF=ahu-01
type egressEnvConfig struct {
	GatewayID        string
	WritablePointID  string
	ReadonlyPointID  string
	WritableLocalID  string
	WritableConnector string
	WritableDeviceRef string
}

func loadEgressEnv() egressEnvConfig {
	get := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	return egressEnvConfig{
		GatewayID:        get("E2E_GATEWAY_ID", "GW-SOS-001"),
		WritablePointID:  get("E2E_WRITABLE_POINT_ID", "SOS-PT-023"),
		ReadonlyPointID:  get("E2E_READONLY_POINT_ID", "SOS-PT-001"),
		WritableLocalID:  get("E2E_WRITABLE_LOCAL_ID", "L-023"),
		WritableConnector: get("E2E_WRITABLE_CONNECTOR", "bacnet-01"),
		WritableDeviceRef: get("E2E_WRITABLE_DEVICE_REF", "SOS-DEV-009"),
	}
}

// TestE2E_BosControlGate verifies M8: the Building OS HTTP API enforces the
// writable flag — writable points return 202, non-writable return 403.
//
// This does not require GatewayEgressService on the BOS side; it exercises the
// HTTP API's authorization gate independently of the gRPC control path.
//
// Run with (SoS BOS defaults):
//
//	E2E_BOS_API_URL=http://192.0.2.20:5000 \
//	  go test ./integration/... -run TestE2E_BosControlGate -v -timeout 30s
//
// For a different BOS deployment, also set E2E_WRITABLE_POINT_ID and E2E_READONLY_POINT_ID.
// The test skips automatically when E2E_BOS_API_URL is unset (normal CI, ADR-0004).
func TestE2E_BosControlGate(t *testing.T) {
	apiBase := os.Getenv("E2E_BOS_API_URL")
	if apiBase == "" {
		t.Skip("E2E_BOS_API_URL not set — set E2E_BOS_API_URL to run")
	}
	cfg := loadEgressEnv()

	t.Run("M8: writable point accepted (202)", func(t *testing.T) {
		statusCode := bosControl(t, apiBase, cfg.WritablePointID, 21.5)
		assert.Contains(t, []int{200, 202}, statusCode,
			"POST /points/%s/control must be accepted by BOS (200 or 202)", cfg.WritablePointID)
	})

	t.Run("M8: non-writable point rejected (403)", func(t *testing.T) {
		statusCode := bosControl(t, apiBase, cfg.ReadonlyPointID, 99.0)
		assert.Equal(t, http.StatusForbidden, statusCode,
			"POST to non-writable point must return 403")
	})
}

// TestE2E_BosEgressDispatch verifies M7: the full control round-trip via gRPC
// GatewayEgressService.Connect — BOS sends ControlCommand, egress.Agent
// dispatches to NATS, mock connector acknowledges, ControlResult returned.
//
// GatewayEgressService is exposed by building-os.gateway-bridge on port 5052
// (not connector-worker on 5051, which only implements GatewayIngressService).
// Use BOS_EGRESS_ADDR=<host>:5052 in production; see feat/split-bos-ingress-egress-addr.
//
// IMPORTANT — single-connection constraint: BOS only allows one egress connection
// per gateway ID. If the nexus-gateway service is running and already connected as
// the same E2E_GATEWAY_ID, BOS will reject the test's egress.Agent with AlreadyExists.
// Stop nexus-gateway before running this test, or set E2E_GATEWAY_ID to a gateway ID
// that is registered in BOS but not currently connected.
//
// Run with:
//
//	E2E_BOS_EGRESS_ADDR=192.0.2.20:5052 E2E_BOS_API_URL=http://192.0.2.20:5000 \
//	E2E_GATEWAY_ID=gw-001 E2E_WRITABLE_POINT_ID=PT006 E2E_READONLY_POINT_ID=PT001 \
//	E2E_WRITABLE_LOCAL_ID=analogValue,1002 E2E_WRITABLE_CONNECTOR=bacnet-01 E2E_WRITABLE_DEVICE_REF=ahu-01 \
//	  go test ./integration/... -run TestE2E_BosEgressDispatch -v -timeout 60s
//
// The test skips automatically when E2E_BOS_EGRESS_ADDR is unset.
//
// Prerequisites on the Building OS side:
//   - building-os.gateway-bridge running (port 5052)
//   - nexus-gateway service NOT connected as E2E_GATEWAY_ID (stop it first)
func TestE2E_BosEgressDispatch(t *testing.T) {
	bosAddr := os.Getenv("E2E_BOS_EGRESS_ADDR")
	if bosAddr == "" {
		t.Skip("E2E_BOS_EGRESS_ADDR not set — requires building-os.gateway-bridge:5052 " +
			"and GatewayConnectionTypes__Map__GW-SOS-001=bacnet-sim on the API server")
	}
	apiBase := os.Getenv("E2E_BOS_API_URL")
	if apiBase == "" {
		apiBase = "http://localhost:5000"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

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

	cfg := loadEgressEnv()

	resolver := pointlist.NewSynced(nil)
	resolver.Update([]pointlist.Entry{
		{
			ConnectorID: cfg.WritableConnector,
			Protocol:    "bacnet",
			LocalID:     cfg.WritableLocalID,
			PointID:     cfg.WritablePointID,
			Writable:    true,
			DeviceRef:   cfg.WritableDeviceRef,
		},
	})

	d := dispatch.New(nc, resolver, 10*time.Second)

	cmdSubject := "cmd.bacnet." + cfg.WritableConnector
	var writeCount atomic.Int64
	sub, err := nc.Subscribe(cmdSubject, func(msg *nats.Msg) {
		writeCount.Add(1)
		reply := dispatch.ConnectorReply{Success: true, Response: "ok"}
		data, _ := json.Marshal(reply)
		_ = msg.Respond(data)
	})
	require.NoError(t, err)
	t.Cleanup(func() { sub.Unsubscribe() })

	agent := egress.New(bosAddr, cfg.GatewayID, d, insecureCreds(), nil)
	go agent.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	t.Run("M7: control command dispatched through gateway to connector", func(t *testing.T) {
		statusCode := bosControl(t, apiBase, cfg.WritablePointID, 21.5)
		assert.Contains(t, []int{200, 202}, statusCode,
			"POST /points/%s/control must be accepted by BOS (200 or 202)", cfg.WritablePointID)

		require.Eventually(t, func() bool {
			return writeCount.Load() >= 1
		}, 20*time.Second, 200*time.Millisecond,
			"mock connector must receive the write command from NATS within 20s")
	})
}

// bosControl issues a control request via the Building OS HTTP API and returns the status code.
func bosControl(t *testing.T, apiBase, pointID string, value float64) int {
	t.Helper()
	url := fmt.Sprintf("%s/points/%s/control", apiBase, pointID)
	payload, _ := json.Marshal(map[string]any{"ControlType": "Hono", "Value": value})
	resp, err := http.Post(url, "application/json", bytes.NewReader(payload)) //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}
