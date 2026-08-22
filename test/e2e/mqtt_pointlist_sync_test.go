// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	pahoClient "github.com/eclipse/paho.golang/paho"
)

// TestMQTTPointListSyncE2E (#119, docs/adr/0008) verifies the full
// Point-List-sync-to-MQTT-subscriptions path against a live stack: an
// operator action (here, the same Admin API call the Admin UI's mqtt-sync
// page's Apply button makes — POST /connectors/{id}/mqtt-subscriptions/apply)
// converges the MQTT connector's live broker subscriptions to the current
// Building OS Point List; a message published on the newly-synced topic is
// received as an MQTT Common Event on the EVENTS stream; and it reaches
// Building OS telemetry (storefwd_sent_total increases) — closing the loop
// this issue exists to close (EP-006/FEAT-026's deferred connector reload).
//
// Requires the local MQTT smoke-test overlay
// (docker-compose.mqtt.yml + docker-compose.mqtt-dev.yml) or an equivalent
// stack, with a real Building OS Point List containing an MQTT point for the
// target connector (see docs/architecture/oss-gateway-pointlist-sync.md and
// docs/guides/onboarding-e2e-gateway.md in gutp-building-os-ri).
//
// Environment:
//
//	E2E_NATS_URL            — required
//	E2E_ADMIN_URL           — gateway Admin API (default http://localhost:8080)
//	E2E_ADMIN_TOKEN         — bearer token for the Admin API, if auth is enabled (default: none)
//	E2E_MQTT_BROKER_ADDR    — MQTT broker host:port for a plain TCP publish (default localhost:11883,
//	                          matching docker-compose.mqtt-dev.yml's host port for mqtt-broker)
//	E2E_MQTT_CONNECTOR_ID   — target MQTT connector id (default mqtt-01)
//	E2E_MQTT_SYNCED_TOPIC   — topic expected to be present in the current Point List
//	                          for E2E_MQTT_CONNECTOR_ID (required — the exact topic
//	                          depends on what's provisioned in Building OS)
func TestMQTTPointListSyncE2E(t *testing.T) {
	_, js := connectNATS(t)
	adminURL := getenvOr("E2E_ADMIN_URL", "http://localhost:8080")
	adminToken := os.Getenv("E2E_ADMIN_TOKEN")
	brokerAddr := getenvOr("E2E_MQTT_BROKER_ADDR", "localhost:11883")
	connID := getenvOr("E2E_MQTT_CONNECTOR_ID", "mqtt-01")
	topic := requireEnv(t, "E2E_MQTT_SYNCED_TOPIC")

	// Operator action: converge the connector's live subscriptions to the
	// current Point List — no restart, the same call the Admin UI makes.
	applyReply := applyMQTTSubscriptions(t, adminURL, adminToken, connID)
	if !applyReply.Applied {
		t.Fatalf("mqtt-subscriptions apply for %s failed: %v", connID, applyReply.Errors)
	}
	t.Logf("mqtt-subscriptions applied: revision=%s subscribed=%d", applyReply.AppliedRevision, applyReply.SubscribedCount)

	before := scrapeGatewayMetrics(t, adminURL)

	// Publish on the newly-synced topic and observe the resulting Common Event.
	payload := fmt.Sprintf(`{"value":%d}`, time.Now().Unix()%1000)
	publishE2EMQTT(t, brokerAddr, topic, []byte(payload))
	ev := awaitCommonEvent(t, js, "mqtt", connID, map[string]bool{topic: true}, 30*time.Second)
	if ev.Protocol != "mqtt" {
		t.Fatalf("protocol = %q, want mqtt", ev.Protocol)
	}
	t.Logf("MQTT telemetry observed: %s = %g %s", ev.LocalID, ev.Value, ev.Unit)

	// It reaches Building OS telemetry, not just the EVENTS stream.
	if !waitForMetricIncrease(t, adminURL, before, "storefwd_sent_total", 30*time.Second) {
		t.Fatal("storefwd_sent_total did not increase — event reached NATS but not Building OS telemetry")
	}
}

// mqttApplyReply mirrors sdk.SubscriptionApplyReply without importing the
// connector package, matching this file's writeReply/commonEvent convention
// (the e2e package only observes the wire).
type mqttApplyReply struct {
	Applied         bool     `json:"applied"`
	AppliedRevision string   `json:"applied_revision"`
	SubscribedCount int      `json:"subscribed_count"`
	Errors          []string `json:"errors,omitempty"`
}

// applyMQTTSubscriptions POSTs to the Admin API's MQTT subscription-sync apply
// endpoint (#119) and decodes the reply.
func applyMQTTSubscriptions(t *testing.T, adminURL, token, connectorID string) mqttApplyReply {
	t.Helper()
	url := adminURL + "/connectors/" + connectorID + "/mqtt-subscriptions/apply"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("build request %s: %v", url, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d", url, resp.StatusCode)
	}
	var reply mqttApplyReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatalf("decode apply reply: %v", err)
	}
	return reply
}

// publishE2EMQTT publishes one QoS 1 message to a real broker over a plain
// TCP connection — mirrors connector/mqtt's own test helpers, kept
// package-local since those are unexported there.
func publishE2EMQTT(t *testing.T, brokerAddr, topic string, payload []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", brokerAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial MQTT broker %s: %v", brokerAddr, err)
	}
	clientID := fmt.Sprintf("e2e-mqtt-pointlist-sync-%d", time.Now().UnixNano())
	c := pahoClient.NewClient(pahoClient.ClientConfig{Conn: conn, ClientID: clientID})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Connect(ctx, &pahoClient.Connect{KeepAlive: 30, ClientID: clientID, CleanStart: true}); err != nil {
		t.Fatalf("connect to MQTT broker %s: %v", brokerAddr, err)
	}
	defer c.Disconnect(&pahoClient.Disconnect{})
	if _, err := c.Publish(ctx, &pahoClient.Publish{Topic: topic, QoS: 1, Payload: payload}); err != nil {
		t.Fatalf("publish to %s: %v", topic, err)
	}
}

// waitForMetricIncrease polls /metrics until key's value has increased past
// before[key], or timeout elapses.
func waitForMetricIncrease(t *testing.T, adminURL string, before map[string]float64, key string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		after := scrapeGatewayMetrics(t, adminURL)
		if diffMetric(before, after, key) > 0 {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
