// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	pahoClient "github.com/eclipse/paho.golang/paho"
	mochiauth "github.com/mochi-mqtt/server/v2/hooks/auth"
	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	natssrv "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mqttconn "nexus-gateway/connector/mqtt"
	"nexus-gateway/internal/common"
)

// TestMQTT_NumericPayload: plain float payload → Common Event on NATS.
func TestMQTT_NumericPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID:   "mqtt-01",
		BrokerURL:     "mqtt://" + brokerAddr,
		ClientID:      "nexus-gw-num",
		KeepAlive:     30,
		SessionExpiry: 60,
		Points: []mqttconn.PointConfig{
			{Topic: "sensors/temp", DeviceRef: "dev:ahu-01", Unit: "Cel"},
		},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	publishMQTT(t, brokerAddr, "sensors/temp", []byte("22.5"))

	evt := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-01")
	assert.Equal(t, "mqtt", evt.Protocol)
	assert.Equal(t, "mqtt-01", evt.ConnectorID)
	assert.Equal(t, "sensors/temp", evt.LocalID)
	assert.InDelta(t, 22.5, evt.Value, 0.001)
	assert.Equal(t, "Cel", evt.Unit)
	assert.Equal(t, "Good", evt.Quality)
}

// TestMQTT_JSONPayload: {"value": N} JSON payload → Common Event.
func TestMQTT_JSONPayload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID:   "mqtt-02",
		BrokerURL:     "mqtt://" + brokerAddr,
		ClientID:      "nexus-gw-json",
		KeepAlive:     30,
		SessionExpiry: 60,
		Points: []mqttconn.PointConfig{
			{Topic: "sensors/co2", DeviceRef: "dev:room-01", Unit: "ppm"},
		},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	payload, _ := json.Marshal(map[string]any{"value": 850.0, "unit": "ppm"})
	publishMQTT(t, brokerAddr, "sensors/co2", payload)

	evt := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-02")
	assert.InDelta(t, 850.0, evt.Value, 0.001)
	assert.Equal(t, "sensors/co2", evt.LocalID)
}

// TestMQTT_UnknownTopicDropped: message on unconfigured topic → no event emitted.
func TestMQTT_UnknownTopicDropped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	// Connector with no configured topics — subscribes to nothing
	conn := mqttconn.New(mqttconn.Config{
		ConnectorID:   "mqtt-03",
		BrokerURL:     "mqtt://" + brokerAddr,
		ClientID:      "nexus-gw-drop",
		KeepAlive:     30,
		SessionExpiry: 60,
		Points:        []mqttconn.PointConfig{},
	}, nc, js)
	go conn.Run(ctx)

	// Publish on a random topic; connector has no subscription
	time.Sleep(200 * time.Millisecond)
	publishMQTT(t, brokerAddr, "unconfigured/topic", []byte("99.0"))

	// Verify no event arrives within 500ms
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		Durable:       "drop-check",
		FilterSubject: "evt.mqtt.mqtt-03",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(500*time.Millisecond))
	require.NoError(t, err)
	count := 0
	for range msgs.Messages() {
		count++
	}
	assert.Equal(t, 0, count, "no events should be emitted for unconfigured topic")
}

// TestMQTT_WriteSuccess: NATS request on cmd subject → broker receives write → success reply.
func TestMQTT_WriteSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID:   "mqtt-w1",
		BrokerURL:     "mqtt://" + brokerAddr,
		ClientID:      "nexus-gw-write1",
		KeepAlive:     30,
		SessionExpiry: 60,
		Points: []mqttconn.PointConfig{
			{
				Topic:           "sensors/temp",
				DeviceRef:       "dev:ahu-01",
				Unit:            "Cel",
				Writable:        true,
				CommandTopic:    "actuators/temp/set",
				PayloadTemplate: `{"present_value": %g}`,
			},
		},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	// Subscribe on broker to capture the written payload
	received := make(chan []byte, 1)
	sub := subscribeOnBroker(t, ctx, brokerAddr, "actuators/temp/set", received)
	defer sub.Disconnect(&pahoClient.Disconnect{})

	// Dispatcher sends a write request via NATS request-reply
	req, _ := json.Marshal(map[string]any{
		"control_id": "ctrl-001",
		"local_id":   "sensors/temp",
		"value":      23.5,
	})
	msg, err := nc.RequestWithContext(ctx, "cmd.mqtt.mqtt-w1", req)
	require.NoError(t, err)

	var reply mqttconn.WriteReply
	require.NoError(t, json.Unmarshal(msg.Data, &reply))
	assert.True(t, reply.Success)
	assert.Equal(t, "ok", reply.Response)

	// Broker received the formatted payload
	select {
	case payload := <-received:
		assert.Equal(t, `{"present_value": 23.5}`, string(payload))
	case <-ctx.Done():
		t.Fatal("broker did not receive write command")
	}
}

// TestMQTT_WriteDedup: same control_id twice → second call returns cached reply without re-publishing.
func TestMQTT_WriteDedup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID:   "mqtt-w2",
		BrokerURL:     "mqtt://" + brokerAddr,
		ClientID:      "nexus-gw-write2",
		KeepAlive:     30,
		SessionExpiry: 60,
		Points: []mqttconn.PointConfig{
			{
				Topic:           "sensors/temp",
				Writable:        true,
				CommandTopic:    "actuators/temp/set",
				PayloadTemplate: `%g`,
			},
		},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	received := make(chan []byte, 10)
	sub := subscribeOnBroker(t, ctx, brokerAddr, "actuators/temp/set", received)
	defer sub.Disconnect(&pahoClient.Disconnect{})

	req, _ := json.Marshal(map[string]any{"control_id": "ctrl-dup", "local_id": "sensors/temp", "value": 10.0})

	// First call
	msg1, err := nc.RequestWithContext(ctx, "cmd.mqtt.mqtt-w2", req)
	require.NoError(t, err)
	var r1 mqttconn.WriteReply
	require.NoError(t, json.Unmarshal(msg1.Data, &r1))
	assert.True(t, r1.Success)

	// Drain received
	<-received

	// Second call with same control_id — should return cached result, not publish again
	msg2, err := nc.RequestWithContext(ctx, "cmd.mqtt.mqtt-w2", req)
	require.NoError(t, err)
	var r2 mqttconn.WriteReply
	require.NoError(t, json.Unmarshal(msg2.Data, &r2))
	assert.True(t, r2.Success)

	// No second publish to broker
	select {
	case <-received:
		t.Fatal("broker received duplicate write — dedup failed")
	case <-time.After(300 * time.Millisecond):
		// expected: no second publish
	}
}

// TestMQTT_TelemetryDuringWrite: telemetry subscriptions continue while a write is in flight.
func TestMQTT_TelemetryDuringWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	brokerAddr := startBroker(t)
	nc, js := startNATS(t)

	conn := mqttconn.New(mqttconn.Config{
		ConnectorID:   "mqtt-w3",
		BrokerURL:     "mqtt://" + brokerAddr,
		ClientID:      "nexus-gw-write3",
		KeepAlive:     30,
		SessionExpiry: 60,
		Points: []mqttconn.PointConfig{
			{
				Topic:           "sensors/temp",
				DeviceRef:       "dev:ahu-01",
				Unit:            "Cel",
				Writable:        true,
				CommandTopic:    "actuators/temp/set",
				PayloadTemplate: `%g`,
			},
		},
	}, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))

	// Telemetry comes in while write is processed
	publishMQTT(t, brokerAddr, "sensors/temp", []byte("21.0"))
	evt := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-w3")
	assert.InDelta(t, 21.0, evt.Value, 0.001)

	// Write request
	req, _ := json.Marshal(map[string]any{"control_id": "ctrl-telem", "local_id": "sensors/temp", "value": 25.0})
	msg, err := nc.RequestWithContext(ctx, "cmd.mqtt.mqtt-w3", req)
	require.NoError(t, err)
	var reply mqttconn.WriteReply
	require.NoError(t, json.Unmarshal(msg.Data, &reply))
	assert.True(t, reply.Success)

	// Telemetry still works after the write
	publishMQTT(t, brokerAddr, "sensors/temp", []byte("22.0"))
	evt2 := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-w3")
	assert.InDelta(t, 22.0, evt2.Value, 0.001)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func startBroker(t *testing.T) string {
	t.Helper()
	s := mochi.New(nil)
	require.NoError(t, s.AddHook(new(mochiauth.AllowHook), nil))

	tcp := listeners.NewTCP(listeners.Config{ID: "t1", Address: "127.0.0.1:0"})
	require.NoError(t, s.AddListener(tcp))

	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })

	require.Eventually(t, func() bool {
		addr := tcp.Address()
		return addr != "" && addr != "127.0.0.1:0"
	}, 3*time.Second, 10*time.Millisecond)

	return tcp.Address()
}

func startNATS(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	ctx := context.Background()
	opts := &natssrv.Options{Port: -1, JetStream: true, StoreDir: t.TempDir()}
	ns, err := natssrv.NewServer(opts)
	require.NoError(t, err)
	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second))
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "EVENTS",
		Subjects: []string{"evt.>"},
		Storage:  jetstream.MemoryStorage,
	})
	require.NoError(t, err)

	return nc, js
}

func publishMQTT(t *testing.T, brokerAddr, topic string, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := dialPaho(ctx, brokerAddr, fmt.Sprintf("test-pub-%d", time.Now().UnixNano()))
	require.NoError(t, err)
	defer c.Disconnect(&pahoClient.Disconnect{})

	_, err = c.Publish(ctx, &pahoClient.Publish{
		Topic:   topic,
		QoS:     0,
		Payload: payload,
	})
	require.NoError(t, err)
}

func subscribeOnBroker(t *testing.T, ctx context.Context, brokerAddr, topic string, ch chan<- []byte) *pahoClient.Client {
	t.Helper()
	conn, err := net.Dial("tcp", brokerAddr)
	require.NoError(t, err)

	clientID := fmt.Sprintf("test-sub-%d", time.Now().UnixNano())
	capturedTopic := topic
	c := pahoClient.NewClient(pahoClient.ClientConfig{
		Conn:     conn,
		ClientID: clientID,
		OnPublishReceived: []func(pahoClient.PublishReceived) (bool, error){
			func(pr pahoClient.PublishReceived) (bool, error) {
				if pr.Packet.Topic == capturedTopic {
					payload := make([]byte, len(pr.Packet.Payload))
					copy(payload, pr.Packet.Payload)
					select {
					case ch <- payload:
					default:
					}
				}
				return true, nil
			},
		},
	})
	_, err = c.Connect(ctx, &pahoClient.Connect{
		KeepAlive:  30,
		ClientID:   clientID,
		CleanStart: true,
	})
	require.NoError(t, err)

	_, err = c.Subscribe(ctx, &pahoClient.Subscribe{
		Subscriptions: []pahoClient.SubscribeOptions{{Topic: topic, QoS: 1}},
	})
	require.NoError(t, err)
	return c
}

func dialPaho(ctx context.Context, addr, clientID string) (*pahoClient.Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	c := pahoClient.NewClient(pahoClient.ClientConfig{
		Conn:     conn,
		ClientID: clientID,
	})
	_, err = c.Connect(ctx, &pahoClient.Connect{
		KeepAlive:  30,
		ClientID:   clientID,
		CleanStart: true,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func consumeOneEvent(t *testing.T, ctx context.Context, js jetstream.JetStream, subject string) common.Event {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		Durable:       "test-" + strings.ReplaceAll(subject, ".", "-"),
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
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
