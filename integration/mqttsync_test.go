// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	pahoClient "github.com/eclipse/paho.golang/paho"
	mochi "github.com/mochi-mqtt/server/v2"
	mochiauth "github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mqttconn "nexus-gateway/connector/mqtt"
	"nexus-gateway/internal/common"
	"nexus-gateway/internal/mqttsync"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/pointsync"
	"nexus-gateway/internal/provisioning"
)

// TestMQTTSync_LiveSubscriptionReload exercises the full gateway-side chain
// for #119: a change picked up by pointsync.Loop against a mock Building OS
// Point List is derived into an MQTT subscription set by internal/mqttsync
// and, once an operator calls Apply, converges a live connector/mqtt.Connector's
// broker subscriptions — with no restart, mirroring TestPointSync_LiveRemap's
// shape for the Normalizer resolver (docs/adr/0003, docs/adr/0008).
func TestMQTTSync_LiveSubscriptionReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ns := startEmbeddedNATS(t)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "EVENTS", Subjects: []string{"evt.>"}, Storage: jetstream.MemoryStorage,
	})
	require.NoError(t, err)

	brokerAddr := startMQTTBroker(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID: "mqtt-sync-01", BrokerURL: "mqtt://" + brokerAddr,
		ClientID: "nexus-gw-sync", KeepAlive: 30,
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	// Point List starts empty for this connector.
	mockAPI := provisioning.NewMock(nil)
	resolver := pointlist.NewSynced(nil)
	loop := pointsync.New(mockAPI, resolver, pointsync.Config{Interval: 20 * time.Millisecond})
	go loop.Run(ctx)
	select {
	case <-loop.Ready():
	case <-ctx.Done():
		t.Fatal("pointsync loop did not become ready")
	}

	syncClient := mqttsync.NewClient(nc, resolver, mqttsync.Config{Timeout: 5 * time.Second})

	// Before any sync: nothing to add, nothing subscribed.
	diff, err := syncClient.Preview(ctx, "mqtt-sync-01")
	require.NoError(t, err)
	assert.Empty(t, diff.Added)

	// Building OS's Point List gains an MQTT point for this connector.
	mockAPI.SetSnapshot([]pointlist.Entry{
		{ConnectorID: "mqtt-sync-01", Protocol: "mqtt", LocalID: "sensors/synced-temp", DeviceRef: "dev-synced"},
	})
	require.Eventually(t, func() bool {
		diff, err := syncClient.Preview(ctx, "mqtt-sync-01")
		return err == nil && len(diff.Added) == 1
	}, 3*time.Second, 20*time.Millisecond, "preview must show the new point as pending once pointsync picks it up")

	reply, err := syncClient.Apply(ctx, "mqtt-sync-01")
	require.NoError(t, err)
	require.True(t, reply.Applied, "errors: %v", reply.Errors)
	assert.Equal(t, 1, reply.SubscribedCount)
	assert.Equal(t, []string{"sensors/synced-temp"}, reply.Added)

	// The connector now delivers telemetry for the synced topic — without
	// having been restarted.
	publishMQTTMsg(t, brokerAddr, "sensors/synced-temp", []byte("42.0"))
	evt := consumeOneMQTTSyncEvent(t, ctx, js, "evt.mqtt.mqtt-sync-01")
	assert.Equal(t, "sensors/synced-temp", evt.LocalID)
	assert.Equal(t, "dev-synced", evt.DeviceRef)

	// A second preview against the now-applied state shows nothing pending.
	diff, err = syncClient.Preview(ctx, "mqtt-sync-01")
	require.NoError(t, err)
	assert.Empty(t, diff.Added)
	assert.Empty(t, diff.Changed)
	assert.Empty(t, diff.Removed)

	// Removing the point from the Point List and re-applying stops telemetry
	// for it, again without a restart.
	mockAPI.SetSnapshot(nil)
	require.Eventually(t, func() bool {
		diff, err := syncClient.Preview(ctx, "mqtt-sync-01")
		return err == nil && len(diff.Removed) == 1
	}, 3*time.Second, 20*time.Millisecond, "preview must show the point as pending removal")

	reply2, err := syncClient.Apply(ctx, "mqtt-sync-01")
	require.NoError(t, err)
	require.True(t, reply2.Applied, "errors: %v", reply2.Errors)
	assert.Equal(t, []string{"sensors/synced-temp"}, reply2.Removed)

	publishMQTTMsg(t, brokerAddr, "sensors/synced-temp", []byte("99.0"))
	assertNoMQTTSyncEvent(t, ctx, js, "evt.mqtt.mqtt-sync-01")
}

// ── MQTT test-broker helpers (mirrors connector/mqtt's own test helpers;
// kept package-local since those are unexported there) ─────────────────────

func startMQTTBroker(t *testing.T) string {
	t.Helper()
	s := mochi.New(nil)
	require.NoError(t, s.AddHook(new(mochiauth.AllowHook), nil))

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

func publishMQTTMsg(t *testing.T, brokerAddr, topic string, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := net.Dial("tcp", brokerAddr)
	require.NoError(t, err)
	c := pahoClient.NewClient(pahoClient.ClientConfig{Conn: conn, ClientID: "mqttsync-test-pub"})
	_, err = c.Connect(ctx, &pahoClient.Connect{KeepAlive: 30, ClientID: "mqttsync-test-pub", CleanStart: true})
	require.NoError(t, err)
	defer c.Disconnect(&pahoClient.Disconnect{})

	_, err = c.Publish(ctx, &pahoClient.Publish{Topic: topic, QoS: 1, Payload: payload})
	require.NoError(t, err)
}

func consumeOneMQTTSyncEvent(t *testing.T, ctx context.Context, js jetstream.JetStream, subject string) common.Event {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		Durable: "mqttsync-test-" + strings.ReplaceAll(subject, ".", "-"), FilterSubject: subject, AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(10*time.Second))
	require.NoError(t, err)
	var evt common.Event
	for msg := range msgs.Messages() {
		require.NoError(t, json.Unmarshal(msg.Data(), &evt))
		_ = msg.Ack()
		return evt
	}
	t.Fatal("no event received within timeout")
	return evt
}

func assertNoMQTTSyncEvent(t *testing.T, ctx context.Context, js jetstream.JetStream, subject string) {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		Durable: "mqttsync-test-" + strings.ReplaceAll(subject, ".", "-"), FilterSubject: subject, AckPolicy: jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(500*time.Millisecond))
	require.NoError(t, err)
	for range msgs.Messages() {
		t.Fatalf("unexpected event on %s", subject)
	}
}
