// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt_test

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mqttconn "nexus-gateway/connector/mqtt"
	"nexus-gateway/connector/sdk"
)

// assertNoMoreEvents fetches from the SAME durable consumer consumeOneEvent
// uses (not assertNoEvent's separate one) and asserts nothing arrives — used
// after a preceding consumeOneEvent on the same subject, so the check
// continues from that consumer's cursor instead of re-reading the backlog a
// fresh durable would start from.
func assertNoMoreEvents(t *testing.T, ctx context.Context, js jetstream.JetStream, subject string) {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		Durable:       "test-" + strings.ReplaceAll(subject, ".", "-"),
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(500*time.Millisecond))
	require.NoError(t, err)
	for range msgs.Messages() {
		t.Fatalf("unexpected event on %s", subject)
	}
}

// applySubscriptions sends a SubscriptionApplyRequest over the connector's
// cfg.mqtt.<id>.apply subject and returns the decoded reply — the same NATS
// request-reply path internal/mqttsync uses in production (#119, docs/adr/0008).
func applySubscriptions(t *testing.T, ctx context.Context, nc *nats.Conn, connectorID string, req sdk.SubscriptionApplyRequest) sdk.SubscriptionApplyReply {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	msg, err := nc.RequestWithContext(ctx, "cfg.mqtt."+connectorID+".apply", data)
	require.NoError(t, err)
	var reply sdk.SubscriptionApplyReply
	require.NoError(t, json.Unmarshal(msg.Data, &reply))
	return reply
}

func subscriptionStatus(t *testing.T, ctx context.Context, nc *nats.Conn, connectorID string) sdk.SubscriptionStatusReply {
	t.Helper()
	msg, err := nc.RequestWithContext(ctx, "cfg.mqtt."+connectorID+".status", nil)
	require.NoError(t, err)
	var status sdk.SubscriptionStatusReply
	require.NoError(t, json.Unmarshal(msg.Data, &status))
	return status
}

// TestMQTT_SubscriptionStatusReportsInitialState: a freshly started connector
// reports its static Points as its subscription state, with revision "".
func TestMQTT_SubscriptionStatusReportsInitialState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID: "mqtt-status", BrokerURL: "mqtt://" + brokerAddr,
		ClientID: "nexus-gw-status", KeepAlive: 30,
		Points: []mqttconn.PointConfig{{Topic: "sensors/temp", DeviceRef: "dev-1", Unit: "Cel"}},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	status := subscriptionStatus(t, ctx, nc, "mqtt-status")
	assert.Equal(t, "", status.AppliedRevision)
	require.Len(t, status.Subscriptions, 1)
	assert.Equal(t, "sensors/temp", status.Subscriptions[0].Topic)
	assert.Equal(t, "dev-1", status.Subscriptions[0].DeviceRef)
	assert.EqualValues(t, 1, status.Subscriptions[0].QoS, "unset QoS defaults to 1")
}

// TestMQTT_ApplySubscriptionsAddChangeRemove: a live apply converges the
// broker session without a restart — added topics start producing telemetry,
// removed topics stop, and a changed point's metadata updates in place (#119).
func TestMQTT_ApplySubscriptionsAddChangeRemove(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID: "mqtt-acr", BrokerURL: "mqtt://" + brokerAddr,
		ClientID: "nexus-gw-acr", KeepAlive: 30,
		Points: []mqttconn.PointConfig{
			{Topic: "sensors/a", DeviceRef: "dev-a"},
			{Topic: "sensors/b", DeviceRef: "dev-b"},
		},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	reply := applySubscriptions(t, ctx, nc, "mqtt-acr", sdk.SubscriptionApplyRequest{
		Revision: "rev1",
		Points: []sdk.SubscriptionSpec{
			{Topic: "sensors/a", QoS: 1, DeviceRef: "dev-a-changed"}, // changed
			{Topic: "sensors/c", QoS: 1, DeviceRef: "dev-c"},         // added
			// sensors/b omitted → removed
		},
	})
	require.True(t, reply.Applied, "errors: %v", reply.Errors)
	assert.Equal(t, "rev1", reply.AppliedRevision)
	assert.Equal(t, 2, reply.SubscribedCount)
	assert.ElementsMatch(t, []string{"sensors/c"}, reply.Added)
	assert.ElementsMatch(t, []string{"sensors/a"}, reply.Changed)
	assert.ElementsMatch(t, []string{"sensors/b"}, reply.Removed)

	// Added topic now produces telemetry.
	publishMQTT(t, brokerAddr, "sensors/c", []byte("1.0"))
	evtC := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-acr")
	assert.Equal(t, "sensors/c", evtC.LocalID)
	assert.Equal(t, "dev-c", evtC.DeviceRef)

	// Changed topic reflects the new metadata.
	publishMQTT(t, brokerAddr, "sensors/a", []byte("2.0"))
	evtA := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-acr")
	assert.Equal(t, "dev-a-changed", evtA.DeviceRef)

	// Removed topic no longer produces telemetry.
	publishMQTT(t, brokerAddr, "sensors/b", []byte("3.0"))
	assertNoMoreEvents(t, ctx, js, "evt.mqtt.mqtt-acr")

	status := subscriptionStatus(t, ctx, nc, "mqtt-acr")
	assert.Equal(t, "rev1", status.AppliedRevision)
	assert.Len(t, status.Subscriptions, 2)
}

// TestMQTT_ApplySubscriptionsSubscribeFailureRetainsPrevious: when the
// broker rejects one of the new subscriptions, the apply must fail without
// touching any existing subscription (docs/adr/0008 — Subscribe additions
// first, commit only if every one of them succeeds).
func TestMQTT_ApplySubscriptionsSubscribeFailureRetainsPrevious(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const forbidden = "sensors/forbidden"
	brokerAddr := startBrokerWithHook(t, &denySubscribeHook{forbidden: forbidden})
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID: "mqtt-fail", BrokerURL: "mqtt://" + brokerAddr,
		ClientID: "nexus-gw-fail", KeepAlive: 30,
		Points: []mqttconn.PointConfig{{Topic: "sensors/ok", DeviceRef: "dev-ok"}},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	reply := applySubscriptions(t, ctx, nc, "mqtt-fail", sdk.SubscriptionApplyRequest{
		Revision: "rev-fail",
		Points: []sdk.SubscriptionSpec{
			{Topic: "sensors/ok", QoS: 1, DeviceRef: "dev-ok"},
			{Topic: forbidden, QoS: 1},
		},
	})
	assert.False(t, reply.Applied)
	assert.NotEmpty(t, reply.Errors)

	// The previously applied state (revision "", single point) is untouched.
	status := subscriptionStatus(t, ctx, nc, "mqtt-fail")
	assert.Equal(t, "", status.AppliedRevision)
	require.Len(t, status.Subscriptions, 1)
	assert.Equal(t, "sensors/ok", status.Subscriptions[0].Topic)

	// The existing subscription still delivers telemetry — the failed apply
	// did not disrupt it.
	publishMQTT(t, brokerAddr, "sensors/ok", []byte("5.0"))
	evt := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-fail")
	assert.Equal(t, "sensors/ok", evt.LocalID)
}

// TestMQTT_ApplySubscriptionsWritableSameTopicNoLocal: a writable point whose
// command_topic equals its subscribe topic must not have its own write
// echoed back to it as telemetry (the MQTT loop docs/adr/0008 guards
// against), while a genuine publish from another client on that same topic
// still comes through normally.
func TestMQTT_ApplySubscriptionsWritableSameTopicNoLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID: "mqtt-noloop", BrokerURL: "mqtt://" + brokerAddr,
		ClientID: "nexus-gw-noloop", KeepAlive: 30,
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	topic := "actuators/valve"
	reply := applySubscriptions(t, ctx, nc, "mqtt-noloop", sdk.SubscriptionApplyRequest{
		Revision: "rev-noloop",
		Points: []sdk.SubscriptionSpec{
			{Topic: topic, QoS: 1, Writable: true, CommandTopic: topic, CommandQoS: 1},
		},
	})
	require.True(t, reply.Applied, "errors: %v", reply.Errors)

	// A write via the NATS control plane publishes to `topic` itself...
	req, _ := json.Marshal(map[string]any{"control_id": "ctrl-noloop", "local_id": topic, "value": 42.0})
	msg, err := nc.RequestWithContext(ctx, "cmd.mqtt.mqtt-noloop", req)
	require.NoError(t, err)
	var writeReply mqttconn.WriteReply
	require.NoError(t, json.Unmarshal(msg.Data, &writeReply))
	require.True(t, writeReply.Success)

	// ...but must NOT be echoed back to the connector as telemetry (would be
	// a loop: the connector's own command read back as a state change).
	assertNoEvent(t, ctx, js, "evt.mqtt.mqtt-noloop")

	// A genuine publish from a different client on the same topic (e.g. the
	// device itself reporting state) must still arrive normally — NoLocal
	// only suppresses the connector's own publishes.
	publishMQTT(t, brokerAddr, topic, []byte("7.0"))
	evt := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-noloop")
	assert.Equal(t, topic, evt.LocalID)
}

// TestMQTT_ApplySubscriptionsWildcardCoveredTopicNotDuplicated: a topic
// already reachable through a static MQTT_SUBSCRIPTIONS wildcard filter must
// not get its own explicit broker Subscribe when synced in, and removing it
// again must not touch the (unrelated, still-needed) wildcard filter itself
// (docs/adr/0008).
func TestMQTT_ApplySubscriptionsWildcardCoveredTopicNotDuplicated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID: "mqtt-wc", BrokerURL: "mqtt://" + brokerAddr,
		ClientID: "nexus-gw-wc", KeepAlive: 30,
		Subscriptions: []mqttconn.SubscriptionConfig{{Filter: "sensors/#", QoS: 1}},
		Points:        []mqttconn.PointConfig{{Topic: "sensors/static", DeviceRef: "dev-static"}},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	reply := applySubscriptions(t, ctx, nc, "mqtt-wc", sdk.SubscriptionApplyRequest{
		Revision: "rev-wc",
		Points: []sdk.SubscriptionSpec{
			{Topic: "sensors/static", QoS: 1, DeviceRef: "dev-static"},
			{Topic: "sensors/synced", QoS: 1, DeviceRef: "dev-synced"}, // covered by "sensors/#"
		},
	})
	require.True(t, reply.Applied, "errors: %v", reply.Errors)
	assert.ElementsMatch(t, []string{"sensors/synced"}, reply.Added)

	// The wildcard-covered topic delivers telemetry once routed via the
	// points map, with no explicit Subscribe needed for it.
	publishMQTT(t, brokerAddr, "sensors/synced", []byte("9.0"))
	evt := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-wc")
	assert.Equal(t, "sensors/synced", evt.LocalID)

	// Remove it again — must not unsubscribe the "sensors/#" filter itself.
	reply2 := applySubscriptions(t, ctx, nc, "mqtt-wc", sdk.SubscriptionApplyRequest{
		Revision: "rev-wc-2",
		Points:   []sdk.SubscriptionSpec{{Topic: "sensors/static", QoS: 1, DeviceRef: "dev-static"}},
	})
	require.True(t, reply2.Applied, "errors: %v", reply2.Errors)
	assert.ElementsMatch(t, []string{"sensors/synced"}, reply2.Removed)

	// No longer routed (removed from the points map)...
	publishMQTT(t, brokerAddr, "sensors/synced", []byte("10.0"))
	assertNoMoreEvents(t, ctx, js, "evt.mqtt.mqtt-wc")

	// ...but the wildcard filter itself is still intact: an unrelated topic
	// it also covers keeps delivering telemetry.
	publishMQTT(t, brokerAddr, "sensors/static", []byte("11.0"))
	evtStatic := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-wc")
	assert.Equal(t, "sensors/static", evtStatic.LocalID)
}

// ── sync-test helpers ───────────────────────────────────────────────────────

// denySubscribeHook denies subscribing to a single forbidden topic filter
// (SUBACK reason >= 0x80), which the paho client surfaces as an error from
// cm.Subscribe — used to exercise the apply-failure path deterministically.
type denySubscribeHook struct {
	mochi.HookBase
	forbidden string
}

func (h *denySubscribeHook) ID() string { return "deny-subscribe" }

func (h *denySubscribeHook) Provides(b byte) bool {
	return b == mochi.OnConnectAuthenticate || b == mochi.OnACLCheck
}

func (h *denySubscribeHook) OnConnectAuthenticate(*mochi.Client, packets.Packet) bool { return true }

func (h *denySubscribeHook) OnACLCheck(_ *mochi.Client, topic string, write bool) bool {
	return write || topic != h.forbidden
}

func startBrokerWithHook(t *testing.T, hook mochi.Hook) string {
	t.Helper()
	s := mochi.New(nil)
	require.NoError(t, s.AddHook(hook, nil))

	tcp := listeners.NewTCP(listeners.Config{ID: "t1", Address: "127.0.0.1:0"})
	require.NoError(t, s.AddListener(tcp))

	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })

	require.Eventually(t, func() bool {
		addr := tcp.Address()
		if addr == "" || addr == "127.0.0.1:0" {
			return false
		}
		probe, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = probe.Close()
		return true
	}, 3*time.Second, 10*time.Millisecond)

	return tcp.Address()
}
