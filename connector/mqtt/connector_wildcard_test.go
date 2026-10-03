// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mqttconn "nexus-gateway/connector/mqtt"
	"nexus-gateway/internal/common"
)

// collectEvents reads up to want events from subject, stopping at the first fetch
// window that yields nothing, so a shortfall shows up as a short slice.
func collectEvents(t *testing.T, ctx context.Context, js jetstream.JetStream, subject string, want int, window time.Duration) []common.Event {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		Durable:       "collect-" + strings.ReplaceAll(subject, ".", "-"),
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)

	var events []common.Event
	for len(events) < want {
		msgs, err := cons.Fetch(want-len(events), jetstream.FetchMaxWait(window))
		require.NoError(t, err)
		got := 0
		for msg := range msgs.Messages() {
			var evt common.Event
			require.NoError(t, json.Unmarshal(msg.Data(), &evt))
			_ = msg.Ack()
			events = append(events, evt)
			got++
		}
		if got == 0 {
			break
		}
	}
	return events
}

// startWildcardConnector runs a connector whose only Points are the given ones.
func startWildcardConnector(t *testing.T, ctx context.Context, id string, cfg mqttconn.Config) (string, jetstream.JetStream, *mqttconn.Connector) {
	t.Helper()
	brokerAddr := startBroker(t)
	nc, js := startNATS(t)
	cfg.ConnectorID, cfg.BrokerURL, cfg.ClientID, cfg.KeepAlive = id, "mqtt://"+brokerAddr, "nexus-gw-"+id, 30
	conn := mqttconn.New(cfg, nc, js)
	go conn.Run(ctx)
	require.NoError(t, conn.AwaitReady(ctx))
	return brokerAddr, js, conn
}

func waitReceived(t *testing.T, conn *mqttconn.Connector, n int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		return metricValue(t, conn.Metrics(), "mqtt_received_total") >= n
	}, 10*time.Second, 20*time.Millisecond)
}

// A wildcard Point such as "#" must emit the concrete topic the broker delivered as
// local_id — never the filter — so the Normalizer can match a Point List entry (#173).
func TestMQTT_WildcardPointEmitsConcreteTopicAsLocalID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	brokerAddr, js, _ := startWildcardConnector(t, ctx, "mqtt-wp", mqttconn.Config{
		Points: []mqttconn.PointConfig{{Topic: "#", DeviceRef: "dev:any", Unit: "Cel"}},
	})

	publishMQTT(t, brokerAddr, "sensors/room1/temp", []byte("21.5"))

	evt := consumeOneEvent(t, ctx, js, "evt.mqtt.mqtt-wp")
	assert.Equal(t, "sensors/room1/temp", evt.LocalID, "concrete topic, never the filter")
	assert.NotEqual(t, "#", evt.LocalID)
	assert.Equal(t, "Cel", evt.Unit, "metadata comes from the matching wildcard Point")
	assert.Equal(t, "dev:any", evt.DeviceRef)
	assert.Equal(t, 21.5, eventNumber(t, evt))
}

// An exactly configured topic keeps its own metadata and is never rate-limited,
// even though a wildcard Point also matches it.
func TestMQTT_ExactTopicTakesPrecedenceOverWildcard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	brokerAddr, js, conn := startWildcardConnector(t, ctx, "mqtt-prec", mqttconn.Config{
		Points: []mqttconn.PointConfig{
			{Topic: "#", Unit: "W"},
			{Topic: "sensors/exact", Unit: "A"},
		},
	})

	publishMQTTBurst(t, brokerAddr, "sensors/exact", 3) // 3 messages inside the wildcard interval
	waitReceived(t, conn, 3)

	events := collectEvents(t, ctx, js, "evt.mqtt.mqtt-prec", 3, time.Second)
	require.Len(t, events, 3, "explicit topics are not throttled")
	for _, e := range events {
		assert.Equal(t, "sensors/exact", e.LocalID)
		assert.Equal(t, "A", e.Unit, "exact Point wins over the wildcard")
	}
	assert.Zero(t, metricValue(t, conn.Metrics(), "mqtt_wildcard_throttled_total"))
}

// A topic that matched only a wildcard Point and repeats inside the interval is
// dropped (a 1 s publisher nobody configured must not load the pipeline); other
// topics are unaffected.
func TestMQTT_WildcardMatchedTopicIsRateLimited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	brokerAddr, js, conn := startWildcardConnector(t, ctx, "mqtt-rl", mqttconn.Config{
		Points: []mqttconn.PointConfig{{Topic: "#"}},
	})

	publishMQTTBurst(t, brokerAddr, "sensors/noisy", 3)
	publishMQTT(t, brokerAddr, "sensors/quiet", []byte("1"))
	waitReceived(t, conn, 4)

	events := collectEvents(t, ctx, js, "evt.mqtt.mqtt-rl", 4, time.Second)
	var topics []string
	for _, e := range events {
		topics = append(topics, e.LocalID)
	}
	assert.ElementsMatch(t, []string{"sensors/noisy", "sensors/quiet"}, topics, "one per topic inside the interval")
	assert.Equal(t, int64(2), metricValue(t, conn.Metrics(), "mqtt_wildcard_throttled_total"))
}

// A negative WildcardMinInterval turns the limit off.
func TestMQTT_WildcardRateLimitCanBeDisabled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	brokerAddr, js, conn := startWildcardConnector(t, ctx, "mqtt-nolimit", mqttconn.Config{
		Points:              []mqttconn.PointConfig{{Topic: "#"}},
		WildcardMinInterval: -1,
	})

	publishMQTTBurst(t, brokerAddr, "sensors/noisy", 3)
	waitReceived(t, conn, 3)

	events := collectEvents(t, ctx, js, "evt.mqtt.mqtt-nolimit", 3, time.Second)
	assert.Len(t, events, 3)
	assert.Zero(t, metricValue(t, conn.Metrics(), "mqtt_wildcard_throttled_total"))
}

// The freshness floor resolves wildcard-received topics the same way: a quiet
// wildcard-matched topic is re-published with its concrete local_id and the
// wildcard Point's metadata, instead of being pruned as unknown (#173).
func TestMQTT_WildcardTopicParticipatesInFreshnessFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	brokerAddr, js, _ := startWildcardConnector(t, ctx, "mqtt-fresh", mqttconn.Config{
		Points:            []mqttconn.PointConfig{{Topic: "plant/#", Unit: "Pa"}},
		FreshnessInterval: 300 * time.Millisecond,
	})

	publishMQTT(t, brokerAddr, "plant/ahu1/pressure", []byte("101.3"))

	events := collectEvents(t, ctx, js, "evt.mqtt.mqtt-fresh", 2, 5*time.Second)
	require.Len(t, events, 2, "the original message and one freshness republish")
	for _, e := range events {
		assert.Equal(t, "plant/ahu1/pressure", e.LocalID)
		assert.Equal(t, "Pa", e.Unit)
		assert.Equal(t, 101.3, eventNumber(t, e))
	}
}
