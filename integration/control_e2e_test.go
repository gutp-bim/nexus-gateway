// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/connector/sdk"
)

// TestE2E_BacnetControl verifies the BACnet control path end-to-end: a
// WriteRequest dispatched to cmd.bacnet.bacnet-01 reaches the connector
// write handler, performs WriteProperty on bbc-sim, and returns success.
// Idempotency is verified by sending the same control_id a second time.
//
// Prerequisite: start the integration stack with the bacnet profile:
//
//	docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile bacnet up -d
//
// Then run:
//
//	E2E_NATS_URL=nats://localhost:14222 go test ./integration/... -run TestE2E_BacnetControl -v -timeout 120s
//
// The test skips automatically when E2E_NATS_URL is unset.
// Not added to per-PR CI (host networking requirement; opt-in only, ADR-0004).
func TestE2E_BacnetControl(t *testing.T) {
	natsURL := os.Getenv("E2E_NATS_URL")
	if natsURL == "" {
		t.Skip("E2E_NATS_URL not set — start the integration stack (bacnet profile) and set E2E_NATS_URL to run")
	}

	nc, err := nats.Connect(natsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(15),
		nats.ReconnectWait(2*time.Second),
	)
	require.NoError(t, err, "connect to NATS at %s", natsURL)
	t.Cleanup(nc.Close)

	req := sdk.WriteRequest{
		ControlID: "ctrl-bacnet-e2e-1",
		LocalID:   "analogValue,1002",
		DeviceRef: "ahu-01",
		Value:     22.5,
		Priority:  8,
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	// Wait for the BACnet connector to subscribe to its cmd subject (may still be starting up).
	var reply sdk.WriteReply
	require.Eventually(t, func() bool {
		msg, rerr := nc.Request("cmd.bacnet.bacnet-01", data, 10*time.Second)
		if rerr != nil {
			return false
		}
		return json.Unmarshal(msg.Data, &reply) == nil
	}, 60*time.Second, 3*time.Second, "BACnet connector did not respond within 60 s (is the bacnet profile running?)")

	assert.True(t, reply.Success, "write must succeed, got response: %q", reply.Response)
	assert.Equal(t, "ok", reply.Response)

	// Idempotency: same control_id must return cached result without re-writing.
	msg2, err := nc.Request("cmd.bacnet.bacnet-01", data, 10*time.Second)
	require.NoError(t, err)
	var reply2 sdk.WriteReply
	require.NoError(t, json.Unmarshal(msg2.Data, &reply2))
	assert.Equal(t, reply, reply2, "duplicate control_id must return cached reply")

	t.Logf("BACnet control E2E: wrote %s=%.1f priority=%d → %s", req.LocalID, req.Value, req.Priority, reply.Response)
}

// TestE2E_OpcUAControl verifies the OPC-UA control path end-to-end: a
// WriteRequest dispatched to cmd.opcua.opcua-01 reaches the connector
// write handler, performs a Write on opcua-sim, and returns success.
// Idempotency is verified by sending the same control_id a second time.
//
// Prerequisite: start the integration stack with the opcua profile:
//
//	docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile opcua up -d
//
// Then run:
//
//	E2E_NATS_URL=nats://localhost:14222 go test ./integration/... -run TestE2E_OpcUAControl -v -timeout 120s
//
// The test skips automatically when E2E_NATS_URL is unset.
// Not added to per-PR CI (opt-in only, ADR-0004).
func TestE2E_OpcUAControl(t *testing.T) {
	natsURL := os.Getenv("E2E_NATS_URL")
	if natsURL == "" {
		t.Skip("E2E_NATS_URL not set — start the integration stack (opcua profile) and set E2E_NATS_URL to run")
	}

	nc, err := nats.Connect(natsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(15),
		nats.ReconnectWait(2*time.Second),
	)
	require.NoError(t, err, "connect to NATS at %s", natsURL)
	t.Cleanup(nc.Close)

	req := sdk.WriteRequest{
		ControlID: "ctrl-opcua-e2e-1",
		LocalID:   "ns=2;s=PT006",
		DeviceRef: "ahu-01",
		Value:     22.5,
		Priority:  8,
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	// Wait for the OPC-UA connector to subscribe to its cmd subject (JVM startup takes time).
	var reply sdk.WriteReply
	require.Eventually(t, func() bool {
		msg, rerr := nc.Request("cmd.opcua.opcua-01", data, 10*time.Second)
		if rerr != nil {
			return false
		}
		return json.Unmarshal(msg.Data, &reply) == nil
	}, 60*time.Second, 3*time.Second, "OPC-UA connector did not respond within 60 s (is the opcua profile running?)")

	assert.True(t, reply.Success, "write must succeed, got response: %q", reply.Response)
	assert.Equal(t, "ok", reply.Response)

	// Idempotency: same control_id must return cached result without re-writing.
	msg2, err := nc.Request("cmd.opcua.opcua-01", data, 10*time.Second)
	require.NoError(t, err)
	var reply2 sdk.WriteReply
	require.NoError(t, json.Unmarshal(msg2.Data, &reply2))
	assert.Equal(t, reply, reply2, "duplicate control_id must return cached reply")

	t.Logf("OPC-UA control E2E: wrote %s=%.1f → %s", req.LocalID, req.Value, reply.Response)
}
