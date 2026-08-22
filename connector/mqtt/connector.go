// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"nexus-gateway/connector/sdk"
	"nexus-gateway/internal/common"
)

// PointConfig describes a single MQTT point: the topic is the native local_id (ADR-0001).
type PointConfig struct {
	Topic           string
	DeviceRef       string
	Unit            string
	Writable        bool   // point accepts write commands
	CommandTopic    string // MQTT topic to publish writes to; required when Writable is true
	PayloadTemplate string // fmt.Sprintf template for the write payload, e.g. `{"present_value": %g}`; defaults to plain float string
	// QoS is the Subscribe QoS for Topic; zero falls back to 1 (matches the
	// pre-#119 hardcoded default).
	QoS byte
	// CommandQoS is the Publish QoS used for writes to CommandTopic; zero
	// falls back to 1. Independent of QoS (docs/adr/0008).
	CommandQoS byte
}

// subscribeQoS returns p.QoS, defaulting to 1 when unset.
func (p PointConfig) subscribeQoS() byte {
	if p.QoS == 0 {
		return 1
	}
	return p.QoS
}

// commandQoS returns p.CommandQoS, defaulting to 1 when unset.
func (p PointConfig) commandQoS() byte {
	if p.CommandQoS == 0 {
		return 1
	}
	return p.CommandQoS
}

// SubscriptionConfig is an MQTT topic filter independent of the exact point
// metadata. Filters may contain MQTT wildcards (+ and #).
type SubscriptionConfig struct {
	Filter string
	QoS    byte
}

// Config holds all settings for one MQTT connector instance.
type Config struct {
	ConnectorID       string
	BrokerURL         string // e.g. "mqtt://localhost:1883"
	ClientID          string
	Username          string
	Password          []byte
	KeepAlive         uint16
	SessionExpiry     uint32 // seconds; 0 = session ends on disconnect
	MaxPayloadBytes   uint64
	TLSConfig         *tls.Config
	Subscriptions     []SubscriptionConfig
	IgnoreTopics      []string
	Points            []PointConfig
	FreshnessInterval time.Duration
	// ReceiveMaximum is the MQTT Receive Maximum advertised in CONNECT: how many
	// QoS>0 messages the broker may have in flight to this connector before it
	// must wait for a PUBACK. Zero uses defaultReceiveMaximum.
	ReceiveMaximum uint16
}

// topicIndex is the atomically-swapped view of what this connector currently
// knows: which topics route to which PointConfig, and which of those topics
// this connector itself explicitly Subscribed to at the broker — as opposed
// to being covered by a static MQTT_SUBSCRIPTIONS wildcard filter, which is
// fixed for the connector's lifetime and never touched by a live apply
// (docs/adr/0008). explicitSubs is what ApplySubscriptions diffs against to
// decide what needs an actual Subscribe/Unsubscribe call.
type topicIndex struct {
	points       map[string]PointConfig
	explicitSubs map[string]paho.SubscribeOptions
	revision     string
}

// newTopicIndex builds a topicIndex from points, skipping an explicit
// Subscribe for any topic already covered by a static wildcard filter.
func newTopicIndex(points []PointConfig, staticWildcards []SubscriptionConfig, revision string) *topicIndex {
	idx := &topicIndex{
		points:       make(map[string]PointConfig, len(points)),
		explicitSubs: make(map[string]paho.SubscribeOptions, len(points)),
		revision:     revision,
	}
	for _, p := range points {
		idx.points[p.Topic] = p
		if !coveredByWildcard(p.Topic, staticWildcards) {
			idx.explicitSubs[p.Topic] = subscribeOptionsFor(p)
		}
	}
	return idx
}

// coveredByWildcard reports whether topic is already reachable through one of
// the connector's static MQTT_SUBSCRIPTIONS filters — including a literal
// (non-wildcard) filter that happens to equal topic exactly.
func coveredByWildcard(topic string, wildcards []SubscriptionConfig) bool {
	for _, w := range wildcards {
		if filterMatches(w.Filter, topic) {
			return true
		}
	}
	return false
}

// subscribeOptionsFor builds the broker Subscribe options for one point,
// setting MQTT5 No Local when the point's command topic is the same as its
// subscribe topic — otherwise a write this connector itself publishes would
// be echoed back to it and mis-recorded as telemetry (docs/adr/0008).
func subscribeOptionsFor(p PointConfig) paho.SubscribeOptions {
	return paho.SubscribeOptions{
		Topic:   p.Topic,
		QoS:     p.subscribeQoS(),
		NoLocal: p.Writable && p.CommandTopic == p.Topic,
	}
}

// pointConfigsOf flattens a topic→PointConfig map into a slice, for feeding
// back into newTopicIndex.
func pointConfigsOf(m map[string]PointConfig) []PointConfig {
	out := make([]PointConfig, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	return out
}

// pointConfigFromSpec converts a Point-List-sync wire spec into the internal
// PointConfig shape (docs/adr/0008).
func pointConfigFromSpec(spec sdk.SubscriptionSpec) PointConfig {
	return PointConfig{
		Topic:           spec.Topic,
		DeviceRef:       spec.DeviceRef,
		Unit:            spec.Unit,
		Writable:        spec.Writable,
		CommandTopic:    spec.CommandTopic,
		PayloadTemplate: spec.PayloadTemplate,
		QoS:             spec.QoS,
		CommandQoS:      spec.CommandQoS,
	}
}

// ValidatePoints checks that every point has a topic and, if writable, a
// command topic. Shared by cmd/mqtt-connector's startup validation and
// internal/mqttsync's pre-apply validation of a Point-List-derived set
// (docs/adr/0008), so both reject the same malformed configuration.
func ValidatePoints(points []PointConfig) error {
	for i, p := range points {
		if p.Topic == "" {
			return fmt.Errorf("point %d: topic must not be empty", i)
		}
		if p.Writable && p.CommandTopic == "" {
			return fmt.Errorf("point %d (%s): writable point requires command_topic", i, p.Topic)
		}
		// Only QoS 0/1 are supported (matches parseSubscriptionEnv's existing
		// MQTT_SUBSCRIPTIONS check in cmd/mqtt-connector) — QoS 2 is documented
		// as unsupported (docs/connector-spec.md) and would otherwise be
		// silently accepted here only to behave inconsistently at the broker.
		if p.QoS > 1 {
			return fmt.Errorf("point %d (%s): qos must be 0 or 1, got %d", i, p.Topic, p.QoS)
		}
		if p.CommandQoS > 1 {
			return fmt.Errorf("point %d (%s): command_qos must be 0 or 1, got %d", i, p.Topic, p.CommandQoS)
		}
	}
	return nil
}

const (
	// defaultReceiveMaximum keeps the broker's in-flight window generous. Left
	// unset, brokers apply their own small default (Mosquitto: 20) and park the
	// rest in a bounded send queue that silently drops on overflow — that is how
	// a burst lost 305 messages before the connector ever saw them (#117).
	defaultReceiveMaximum uint16 = 1024

	// publishQueueSize bounds the hand-off from the MQTT receive goroutine to the
	// JetStream publisher, matching the gateway's fan-in hand-off (cmd/gateway).
	// A full queue blocks the receive goroutine on purpose: the backpressure then
	// reaches the broker as MQTT flow control instead of being absorbed by the
	// broker's droppable queue.
	publishQueueSize = 256

	// drainTimeout bounds the shutdown flush of that queue (see drainQueue).
	drainTimeout = 5 * time.Second
)

// pendingPublish is one decoded broker message waiting to be published to
// JetStream. ack is deferred to the publisher so the PUBACK still follows the
// JetStream ack (EnableManualAcknowledgment, QoS 1 at-least-once).
type pendingPublish struct {
	data      []byte
	topic     string
	value     float64
	hasValue  bool
	timestamp time.Time
	ack       func()
}

// WriteReply is re-exported from connector/sdk for callers that import this package.
type WriteReply = sdk.WriteReply

// Connector subscribes to an MQTT broker and publishes Common Events to NATS JetStream
// on subject evt.mqtt.<connector_id> (ADR-0001, ADR-0005).
// It also handles write commands arriving on cmd.mqtt.<connector_id> via NATS request-reply
// and publishes them to the broker (ADR-0004).
type Connector struct {
	cfg       Config
	nc        *nats.Conn
	js        jetstream.JetStream
	readyOnce sync.Once
	ready     chan struct{}
	connected atomic.Bool
	dedup     *sdk.CommandDedup
	lkvMu     sync.Mutex
	lkv       map[string]*lkvState

	// topics is the atomically-swapped live subscription state (#119,
	// docs/adr/0008). Run() stores the initial snapshot; ApplySubscriptions
	// swaps in a new one after successfully converging the broker session.
	topics atomic.Pointer[topicIndex]
	// applyMu serializes ApplySubscriptions calls so two concurrent apply
	// requests cannot interleave their Subscribe/Unsubscribe/Store steps.
	applyMu sync.Mutex

	// Ingest counters served at /metrics (#117). received is incremented at the
	// very top of the receive callback — before the ignore/unknown-topic filters —
	// so a received/published gap is visible instead of the connector reporting
	// only what it chose to forward.
	received      atomic.Int64
	published     atomic.Int64
	publishErrors atomic.Int64
}

// Metrics reports the connector's ingest counters for the /metrics surface the
// SDK health server exposes.
func (c *Connector) Metrics() []sdk.Metric {
	return []sdk.Metric{
		{
			Name: "mqtt_received_total", Type: "counter",
			Help:  "MQTT messages received from the broker, before topic filtering.",
			Value: c.received.Load(),
		},
		{
			Name: "mqtt_published_total", Type: "counter",
			Help:  "Common Events published to JetStream from received broker messages.",
			Value: c.published.Load(),
		},
		{
			Name: "mqtt_publish_error_total", Type: "counter",
			Help:  "JetStream publish failures; the message is left unacked for QoS 1 redelivery.",
			Value: c.publishErrors.Load(),
		},
	}
}

// Healthy reports whether the MQTT broker session is currently connected.
func (c *Connector) Healthy() bool { return c.connected.Load() }

type lkvState struct {
	value    float64
	lastEmit time.Time
}

func New(cfg Config, nc *nats.Conn, js jetstream.JetStream) *Connector {
	return &Connector{
		cfg:   cfg,
		nc:    nc,
		js:    js,
		ready: make(chan struct{}),
		dedup: sdk.NewCommandDedup(1000),
		lkv:   make(map[string]*lkvState),
	}
}

func (c *Connector) dueForRepublish(now time.Time) []string {
	if c.cfg.FreshnessInterval <= 0 {
		return nil
	}
	c.lkvMu.Lock()
	defer c.lkvMu.Unlock()
	var due []string
	for topic, state := range c.lkv {
		if now.Sub(state.lastEmit) >= c.cfg.FreshnessInterval {
			due = append(due, topic)
		}
	}
	return due
}

func (c *Connector) recordValue(topic string, value float64, timestamp time.Time) {
	c.lkvMu.Lock()
	c.lkv[topic] = &lkvState{value: value, lastEmit: timestamp}
	c.lkvMu.Unlock()
}

// runFreshnessFloor re-publishes each point's last-known value once per
// interval so a static MQTT point still meets the 1-minute acquisition cadence
// (connector-spec §3.6). It reads c.topics fresh on every republish rather
// than a snapshot taken at Run() startup, so a point added by a live
// ApplySubscriptions (#119) gets its correct metadata immediately.
func (c *Connector) runFreshnessFloor(ctx context.Context, subject string) {
	ticker := time.NewTicker(c.cfg.FreshnessInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, topic := range c.dueForRepublish(now) {
				point, ok := c.topics.Load().points[topic]
				if !ok {
					// ApplySubscriptions (#119) removed this topic from routing,
					// but a prior recordValue() call already seeded c.lkv for it —
					// prune it here instead of republishing a Common Event with
					// zero-value device_ref/unit for a point that's no longer synced.
					c.lkvMu.Lock()
					delete(c.lkv, topic)
					c.lkvMu.Unlock()
					continue
				}
				c.lkvMu.Lock()
				state := c.lkv[topic]
				if state == nil || now.Sub(state.lastEmit) < c.cfg.FreshnessInterval {
					c.lkvMu.Unlock()
					continue
				}
				value := state.value
				state.lastEmit = now
				c.lkvMu.Unlock()
				event := common.Event{Protocol: "mqtt", ConnectorID: c.cfg.ConnectorID, LocalID: topic, DeviceRef: point.DeviceRef, Value: common.NumberValue(value), Unit: point.Unit, Quality: "Good", Timestamp: now.Format(time.RFC3339)}
				if data, err := json.Marshal(event); err == nil {
					_, _ = c.js.Publish(ctx, subject, data)
				}
			}
		}
	}
}

// runPublisher drains the hand-off queue, publishing each decoded message to
// JetStream and only then acknowledging it to the broker. Doing this off the
// MQTT receive goroutine is what keeps the broker delivering during an uplink
// stall (#117). On ctx cancellation it flushes whatever is still queued before
// returning — see drainQueue for why that flush is mandatory rather than leaving
// the messages to QoS 1 redelivery.
func (c *Connector) runPublisher(ctx context.Context, subject string, queue <-chan pendingPublish) {
	for {
		select {
		case <-ctx.Done():
			c.drainQueue(subject, queue)
			return
		case pending := <-queue:
			// select picks randomly among ready cases, so this arm still wins
			// sometimes after cancellation. Publishing under the dead context would
			// fail outright and lose the frame, so carry it into the flush instead.
			if ctx.Err() != nil {
				c.drainQueue(subject, queue, pending)
				return
			}
			if !c.publishPending(ctx, subject, pending) {
				// Cancelled mid-publish — re-flush this frame on a fresh context
				// along with whatever else is still queued.
				c.drainQueue(subject, queue, pending)
				return
			}
		}
	}
}

// drainQueue flushes the messages still buffered when the publisher is stopped.
// This is not optional: a queued message has already been taken from the broker
// but not yet PUBACKed, and under the default MQTT_SESSION_EXPIRY=0 the session
// ends at disconnect, so the broker discards its unacked QoS 1 state instead of
// redelivering. Without this flush a graceful restart would silently lose up to
// publishQueueSize frames (docs/connector-spec.md §5.6 "flush pending publishes").
//
// It runs on a fresh bounded context because the publisher's own context is
// already cancelled and would fail every publish. The MQTT connection is usually
// gone by this point so the PUBACKs are best-effort — but the frames still reach
// JetStream, which is the loss that actually matters.
// carried holds frames already taken off the queue by the caller, which are
// flushed ahead of the remaining backlog.
func (c *Connector) drainQueue(subject string, queue <-chan pendingPublish, carried ...pendingPublish) {
	if len(queue) == 0 && len(carried) == 0 {
		return
	}
	slog.Info("mqtt: flushing pending publishes on shutdown", "count", len(queue)+len(carried))

	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	for _, pending := range carried {
		if !c.publishPending(ctx, subject, pending) {
			slog.Warn("mqtt: shutdown flush incomplete — remaining frames dropped", "remaining", len(queue))
			return
		}
	}
	for {
		select {
		case pending := <-queue:
			if !c.publishPending(ctx, subject, pending) {
				slog.Warn("mqtt: shutdown flush incomplete — remaining frames dropped", "remaining", len(queue))
				return
			}
		default:
			return
		}
	}
}

// publishDirect flushes a single frame that never made it onto the queue because
// the publisher had already stopped. Same bounded-context reasoning as drainQueue.
func (c *Connector) publishDirect(subject string, pending pendingPublish) {
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if !c.publishPending(ctx, subject, pending) {
		slog.Warn("mqtt: shutdown flush timed out — frame dropped", "topic", pending.topic)
	}
}

// publishPending publishes one queued message and acks it to the broker on
// success. It reports false when ctx died mid-publish — the frame is not at fault
// there, so the caller re-flushes it on a fresh context instead of counting a
// spurious error. A genuine publish failure returns true: the frame stays unacked
// for QoS 1 redelivery and the caller carries on.
func (c *Connector) publishPending(ctx context.Context, subject string, pending pendingPublish) bool {
	if _, err := c.js.Publish(ctx, subject, pending.data); err != nil {
		if ctx.Err() != nil {
			return false
		}
		c.publishErrors.Add(1)
		slog.Warn("mqtt: nats publish failed — withholding PUBACK for QoS 1 retry", "err", err)
		// Do not ack: broker will redeliver when NATS is available again.
		return true
	}
	c.published.Add(1)
	if pending.hasValue {
		c.recordValue(pending.topic, pending.value, pending.timestamp)
	}
	pending.ack()
	return true
}

// AwaitReady blocks until the first MQTT subscription is active or ctx is cancelled.
// Use this in tests and startup sequences instead of time.Sleep.
func (c *Connector) AwaitReady(ctx context.Context) error {
	select {
	case <-c.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run connects to the MQTT broker and processes messages until ctx is cancelled.
// autopaho handles reconnection automatically.
func (c *Connector) Run(ctx context.Context) {
	ignoredTopics := make(map[string]struct{}, len(c.cfg.IgnoreTopics))
	for _, topic := range c.cfg.IgnoreTopics {
		ignoredTopics[topic] = struct{}{}
	}
	// c.cfg.Subscriptions (the static MQTT_SUBSCRIPTIONS wildcard filters) is
	// fixed for the life of the connection; every topicIndex built during this
	// Run (initial and every later ApplySubscriptions) is diffed against it
	// (docs/adr/0008).
	c.topics.Store(newTopicIndex(c.cfg.Points, c.cfg.Subscriptions, ""))

	brokerURL, err := url.Parse(c.cfg.BrokerURL)
	if err != nil {
		slog.Error("mqtt: invalid broker URL", "url", c.cfg.BrokerURL, "err", err)
		return
	}

	subject := "evt.mqtt." + c.cfg.ConnectorID

	// Register write handler before starting the connection so it is live before
	// readyOnce fires (OnConnectionUp runs on autopaho's internal goroutine).
	// cm is published atomically: the NATS callback may fire (and Load) on another
	// goroutine concurrently with the Store below once NewConnection returns.
	var cm atomic.Pointer[autopaho.ConnectionManager]
	sub, err := c.nc.Subscribe("cmd.mqtt."+c.cfg.ConnectorID, func(msg *nats.Msg) {
		// Each write runs in its own goroutine so the NATS dispatch goroutine is
		// never blocked by the up-to-8 s cm.Publish call.
		go c.handleWrite(ctx, cm.Load(), msg)
	})
	if err != nil {
		slog.Error("mqtt: write handler subscribe failed", "err", err)
		return
	}
	defer sub.Unsubscribe()

	// Point-List-sync control plane (#119, docs/adr/0008): status reports the
	// currently applied subscription state; apply converges it to a new set.
	// Registered before the connection starts, matching the write handler above.
	statusSub, err := c.nc.Subscribe("cfg.mqtt."+c.cfg.ConnectorID+".status", func(msg *nats.Msg) {
		data, err := json.Marshal(c.SubscriptionStatus())
		if err != nil {
			return
		}
		_ = msg.Respond(data)
	})
	if err != nil {
		slog.Error("mqtt: subscription-status handler subscribe failed", "err", err)
		return
	}
	defer statusSub.Unsubscribe()

	applySub, err := c.nc.Subscribe("cfg.mqtt."+c.cfg.ConnectorID+".apply", func(msg *nats.Msg) {
		go c.handleSubscriptionApply(ctx, cm.Load(), msg)
	})
	if err != nil {
		slog.Error("mqtt: subscription-apply handler subscribe failed", "err", err)
		return
	}
	defer applySub.Unsubscribe()

	// The publisher gets its own context so Run can stop it on every exit path —
	// including a connection manager that finished while the caller's ctx is still
	// live — and never return while it is still running. Stopping it flushes the
	// queue (drainQueue) rather than relying on QoS 1 redelivery, which the default
	// MQTT_SESSION_EXPIRY=0 does not provide.
	pubCtx, stopPublisher := context.WithCancel(ctx)
	queue := make(chan pendingPublish, publishQueueSize)
	publisherDone := make(chan struct{})
	go func() {
		defer close(publisherDone)
		c.runPublisher(pubCtx, subject, queue)
	}()
	defer func() {
		stopPublisher()
		<-publisherDone
		// A receive callback can still have been mid-enqueue when the publisher
		// drained, so sweep once more now that nothing else consumes the queue.
		c.drainQueue(subject, queue)
	}()

	mgr, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{brokerURL},
		TlsCfg:                        c.cfg.TLSConfig,
		KeepAlive:                     c.cfg.KeepAlive,
		CleanStartOnInitialConnection: false,
		SessionExpiryInterval:         c.cfg.SessionExpiry,
		ConnectRetryDelay:             5 * time.Second,
		ConnectUsername:               c.cfg.Username,
		ConnectPassword:               c.cfg.Password,
		// Advertise an explicit Receive Maximum so the broker throttles itself to
		// our unacked window instead of queueing (and then dropping) messages we
		// never see (#117). autopaho only allocates Properties when a session
		// expiry is configured, so cover the nil case.
		ConnectPacketBuilder: func(cp *paho.Connect, _ *url.URL) (*paho.Connect, error) {
			receiveMaximum := c.receiveMaximum()
			if cp.Properties == nil {
				// RequestProblemInfo defaults to 1 on the wire; a zero-value
				// struct would silently switch broker reason strings off.
				cp.Properties = &paho.ConnectProperties{RequestProblemInfo: true}
			}
			cp.Properties.ReceiveMaximum = &receiveMaximum
			return cp, nil
		},
		OnConnectError: func(err error) {
			c.connected.Store(false)
			slog.Warn("mqtt: broker connection failed", "broker", c.cfg.BrokerURL, "err", err)
		},
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			// Read fresh on every (re)connect — not a value captured once before
			// NewConnection — so a reconnect after a live ApplySubscriptions (#119)
			// resubscribes the CURRENT topic set, not the one Run() started with.
			subs := c.brokerSubscriptions()
			if len(subs) > 0 {
				if _, err := cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: subs}); err != nil {
					slog.Error("mqtt: subscribe failed — disconnecting to trigger retry", "err", err)
					// Disconnect from a new goroutine: calling Disconnect from within
					// OnConnectionUp (which runs on autopaho's internal goroutine) risks
					// a deadlock. The reconnect loop will call OnConnectionUp again.
					go func() { _ = cm.Disconnect(ctx) }()
					return
				}
			}
			slog.Info("mqtt: connected and subscriptions active", "broker", c.cfg.BrokerURL, "subscriptions", len(subs))
			c.connected.Store(true)
			// Signal that the first subscription is ready (subsequent reconnects are silently ignored).
			c.readyOnce.Do(func() { close(c.ready) })
		},
		ClientConfig: paho.ClientConfig{
			ClientID: c.cfg.ClientID,
			// Manual acknowledgment: PUBACK is sent only after the event lands in JetStream,
			// preventing data loss when NATS is temporarily unavailable (QoS 1 guarantee).
			EnableManualAcknowledgment: true,
			OnServerDisconnect:         func(*paho.Disconnect) { c.connected.Store(false) },
			OnClientError:              func(error) { c.connected.Store(false) },
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					c.received.Add(1)
					if _, ignored := ignoredTopics[pr.Packet.Topic]; ignored {
						_ = pr.Client.Ack(pr.Packet)
						return true, nil
					}
					p, ok := c.topics.Load().points[pr.Packet.Topic]
					if !ok {
						// Unknown topic: ack immediately to avoid infinite broker retry.
						_ = pr.Client.Ack(pr.Packet)
						return true, nil
					}
					if uint64(len(pr.Packet.Payload)) > c.maxPayloadBytes() {
						slog.Warn("mqtt: payload exceeds size limit", "topic", pr.Packet.Topic,
							"size", len(pr.Packet.Payload), "limit", c.maxPayloadBytes())
						_ = pr.Client.Ack(pr.Packet)
						return true, nil
					}
					decoded, outcome := decodePayload(pr.Packet.Payload, time.Now())
					if outcome == payloadIgnored {
						_ = pr.Client.Ack(pr.Packet)
						return true, nil
					}
					if outcome != payloadTelemetry {
						slog.Warn("mqtt: unparseable payload", "topic", pr.Packet.Topic)
						// Unparseable: ack to avoid infinite retry; event cannot be used.
						_ = pr.Client.Ack(pr.Packet)
						return true, nil
					}
					evt := common.Event{
						Protocol:    "mqtt",
						ConnectorID: c.cfg.ConnectorID,
						LocalID:     p.Topic,
						DeviceRef:   p.DeviceRef,
						Value:       decoded.Value,
						Unit:        p.Unit,
						Quality:     "Good",
						Timestamp:   decoded.Timestamp.Format(time.RFC3339),
					}
					data, err := json.Marshal(evt)
					if err != nil {
						_ = pr.Client.Ack(pr.Packet)
						return true, nil
					}
					// Hand the JetStream publish (and the PUBACK that follows it)
					// to the publisher goroutine. Blocking this callback on the
					// uplink is what let the broker's outgoing queue overflow and
					// drop messages before the gateway saw them (#117).
					client, packet := pr.Client, pr.Packet
					pending := pendingPublish{
						data:      data,
						topic:     packet.Topic,
						timestamp: decoded.Timestamp,
						ack:       func() { _ = client.Ack(packet) },
					}
					pending.value, pending.hasValue = decoded.Value.Number()
					// Once the publisher is stopping, hand nothing else to the queue:
					// select picks randomly among ready cases, so an enqueue here can
					// land after the flush already drained and strand the frame.
					if pubCtx.Err() != nil {
						c.publishDirect(subject, pending)
						return true, nil
					}
					select {
					case queue <- pending:
					case <-pubCtx.Done():
						// Queue was full and the publisher stopped while we waited, so
						// nobody will pick this up — flush it here instead.
						c.publishDirect(subject, pending)
					}
					return true, nil
				},
			},
		},
	})
	if err != nil {
		slog.Error("mqtt: connection manager init failed", "err", err)
		return
	}
	cm.Store(mgr)
	if c.cfg.FreshnessInterval > 0 {
		go c.runFreshnessFloor(ctx, subject)
	}

	<-mgr.Done()
}

// brokerSubscriptions returns everything that must be (re-)subscribed after a
// connect: the static MQTT_SUBSCRIPTIONS wildcard filters (fixed for the
// connector's lifetime) plus every topic this connector currently explicitly
// owns, read fresh from c.topics (docs/adr/0008).
func (c *Connector) brokerSubscriptions() []paho.SubscribeOptions {
	idx := c.topics.Load()
	subs := make([]paho.SubscribeOptions, 0, len(c.cfg.Subscriptions)+len(idx.explicitSubs))
	for _, w := range c.cfg.Subscriptions {
		subs = append(subs, paho.SubscribeOptions{Topic: w.Filter, QoS: w.QoS})
	}
	for _, opt := range idx.explicitSubs {
		subs = append(subs, opt)
	}
	return subs
}

// SubscriptionStatus reports the connector's currently applied subscription
// state (#119) — served over NATS on cfg.mqtt.<connector_id>.status.
func (c *Connector) SubscriptionStatus() sdk.SubscriptionStatusReply {
	idx := c.topics.Load()
	specs := make([]sdk.SubscriptionSpec, 0, len(idx.points))
	for _, p := range idx.points {
		spec := sdk.SubscriptionSpec{
			Topic:           p.Topic,
			QoS:             p.subscribeQoS(),
			Writable:        p.Writable,
			CommandTopic:    p.CommandTopic,
			DeviceRef:       p.DeviceRef,
			Unit:            p.Unit,
			PayloadTemplate: p.PayloadTemplate,
		}
		// CommandQoS only means something alongside a command topic — leave it
		// at the zero value otherwise so a read-only point's reported status
		// matches what mqttsync.Derive produces for it (mqttsync/derive.go
		// only sets CommandQoS for a writable point too), and a diff between
		// the two never spuriously reports "changed" (#119).
		if p.Writable {
			spec.CommandQoS = p.commandQoS()
		}
		specs = append(specs, spec)
	}
	return sdk.SubscriptionStatusReply{AppliedRevision: idx.revision, Subscriptions: specs}
}

// handleSubscriptionApply decodes a SubscriptionApplyRequest off NATS and
// replies with the result of ApplySubscriptions. Run in its own goroutine
// (like handleWrite) so the NATS dispatch goroutine is never blocked on a
// broker round-trip.
func (c *Connector) handleSubscriptionApply(ctx context.Context, cm *autopaho.ConnectionManager, msg *nats.Msg) {
	var req sdk.SubscriptionApplyRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		data, _ := json.Marshal(sdk.SubscriptionApplyReply{Errors: []string{"bad_request"}})
		_ = msg.Respond(data)
		return
	}
	reply := c.ApplySubscriptions(ctx, cm, req)
	data, err := json.Marshal(reply)
	if err != nil {
		return
	}
	_ = msg.Respond(data)
}

// ApplySubscriptions converges the live broker session to req.Points (#119).
// Additions and changes are Subscribed first; only once every one of them
// succeeds is the new state committed (c.topics swapped, AppliedRevision
// bumped) and removals Unsubscribed. A Subscribe failure aborts before
// touching anything, so the previously applied subscriptions are always
// retained on failure (docs/adr/0008).
func (c *Connector) ApplySubscriptions(ctx context.Context, cm *autopaho.ConnectionManager, req sdk.SubscriptionApplyRequest) sdk.SubscriptionApplyReply {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	if cm == nil {
		return sdk.SubscriptionApplyReply{Errors: []string{"not_connected"}}
	}

	current := c.topics.Load()
	desired := make(map[string]PointConfig, len(req.Points))
	for _, spec := range req.Points {
		desired[spec.Topic] = pointConfigFromSpec(spec)
	}

	// cfg.mqtt.<id>.apply is a control surface reachable by anything on the
	// deployment's NATS bus, not just the trusted gateway's own mqttsync
	// client (which already validates before sending) — reject a malformed
	// request here too rather than trusting the caller and ending up with an
	// inconsistent in-memory topicIndex or subscribing to invalid topics.
	if err := ValidatePoints(pointConfigsOf(desired)); err != nil {
		return sdk.SubscriptionApplyReply{Errors: []string{"invalid_request: " + err.Error()}}
	}

	var added, changed, removed []string
	for topic, p := range desired {
		if old, ok := current.points[topic]; !ok {
			added = append(added, topic)
		} else if old != p {
			changed = append(changed, topic)
		}
	}
	for topic := range current.points {
		if _, ok := desired[topic]; !ok {
			removed = append(removed, topic)
		}
	}

	next := newTopicIndex(pointConfigsOf(desired), c.cfg.Subscriptions, req.Revision)

	// Subscribe additions+changes not already covered by a static wildcard
	// filter. A topic whose QoS/NoLocal shape is unchanged is a harmless
	// re-subscribe — the broker just updates its existing subscription.
	var toSubscribe []paho.SubscribeOptions
	for _, topic := range added {
		if opt, ok := next.explicitSubs[topic]; ok {
			toSubscribe = append(toSubscribe, opt)
		}
	}
	for _, topic := range changed {
		if opt, ok := next.explicitSubs[topic]; ok {
			toSubscribe = append(toSubscribe, opt)
		}
	}
	if len(toSubscribe) > 0 {
		if _, err := cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: toSubscribe}); err != nil {
			slog.Error("mqtt: subscription apply failed — retaining previous subscriptions", "revision", req.Revision, "err", err)
			return sdk.SubscriptionApplyReply{Errors: []string{"subscribe: " + err.Error()}}
		}
	}

	// Only unsubscribe topics this connector itself explicitly subscribed to —
	// one covered by a static wildcard was never subscribed on its own, so
	// there is nothing of ours to remove (docs/adr/0008).
	var toUnsubscribe []string
	for _, topic := range removed {
		if _, wasExplicit := current.explicitSubs[topic]; wasExplicit {
			toUnsubscribe = append(toUnsubscribe, topic)
		}
	}
	var errs []string
	if len(toUnsubscribe) > 0 {
		if _, err := cm.Unsubscribe(ctx, &paho.Unsubscribe{Topics: toUnsubscribe}); err != nil {
			// Best-effort: the state is already committed below, and a broker-side
			// leftover subscription is harmless — OnPublishReceived acks and drops
			// anything not in the new c.topics.points.
			slog.Warn("mqtt: unsubscribe failed during apply — broker-side subscription left in place", "err", err, "topics", toUnsubscribe)
			errs = append(errs, "unsubscribe: "+err.Error())
		}
	}

	c.topics.Store(next)
	slog.Info("mqtt: subscriptions applied", "revision", req.Revision, "added", len(added), "changed", len(changed), "removed", len(removed))

	return sdk.SubscriptionApplyReply{
		Applied:         true,
		AppliedRevision: req.Revision,
		SubscribedCount: len(next.points),
		Added:           added,
		Changed:         changed,
		Removed:         removed,
		Errors:          errs,
	}
}

func (c *Connector) maxPayloadBytes() uint64 {
	if c.cfg.MaxPayloadBytes == 0 {
		return 1024
	}
	return c.cfg.MaxPayloadBytes
}

func (c *Connector) receiveMaximum() uint16 {
	if c.cfg.ReceiveMaximum == 0 {
		return defaultReceiveMaximum
	}
	return c.cfg.ReceiveMaximum
}

func (c *Connector) handleWrite(ctx context.Context, cm *autopaho.ConnectionManager, msg *nats.Msg) {
	var req sdk.WriteRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		respond(msg, WriteReply{Success: false, Response: "bad_request"})
		return
	}

	// The connection may not be established yet (a command can arrive between the
	// NATS subscribe and cm.Store). Fail fast rather than dereference a nil manager.
	if cm == nil {
		respond(msg, WriteReply{Success: false, Response: "not_connected"})
		return
	}

	// Reserve the slot via CommandDedup. nil-sentinel = in-flight; non-nil = cached.
	proceed, cached := c.dedup.TryReserve(req.ControlID)
	if !proceed {
		if cached == nil {
			// Another goroutine is in-flight; dispatcher will retry.
			respond(msg, WriteReply{Success: false, Response: "in_flight"})
		} else {
			respond(msg, *cached)
		}
		return
	}

	p, ok := c.topics.Load().points[req.LocalID]
	if !ok || !p.Writable || p.CommandTopic == "" {
		reply := WriteReply{Success: false, Response: "not_writable"}
		c.dedup.Complete(req.ControlID, reply)
		respond(msg, reply)
		return
	}

	payload := formatPayload(p.PayloadTemplate, req.Value)

	// Use a bounded timeout so we never block past the dispatcher's deadline.
	wCtx, wCancel := context.WithTimeout(ctx, 8*time.Second)
	defer wCancel()

	_, err := cm.Publish(wCtx, &paho.Publish{
		Topic:   p.CommandTopic,
		QoS:     p.commandQoS(),
		Payload: payload,
	})

	var reply WriteReply
	if err != nil {
		reply = WriteReply{Success: false, Response: "device_error: " + err.Error()}
	} else {
		reply = WriteReply{Success: true, Response: "ok"}
	}
	c.dedup.Complete(req.ControlID, reply)
	respond(msg, reply)
}

func respond(msg *nats.Msg, reply WriteReply) {
	data, _ := json.Marshal(reply)
	_ = msg.Respond(data)
}

func formatPayload(tmpl string, value float64) []byte {
	plain := []byte(strconv.FormatFloat(value, 'g', -1, 64))
	if tmpl == "" {
		return plain
	}
	result := fmt.Sprintf(tmpl, value)
	// fmt.Sprintf embeds "%!verb(type=value)" when the verb is wrong for the arg type.
	// Fall back to plain float rather than sending malformed bytes to the device.
	if strings.Contains(result, "%!") {
		slog.Warn("mqtt: bad PayloadTemplate verb — falling back to plain float", "template", tmpl)
		return plain
	}
	return []byte(result)
}
