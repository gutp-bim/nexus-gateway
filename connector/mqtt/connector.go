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

func (c *Connector) runFreshnessFloor(ctx context.Context, subject string, points map[string]PointConfig) {
	ticker := time.NewTicker(c.cfg.FreshnessInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, topic := range c.dueForRepublish(now) {
				point := points[topic]
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
	c.publishPending(ctx, subject, pending)
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
	topicMap := make(map[string]PointConfig, len(c.cfg.Points))
	ignoredTopics := make(map[string]struct{}, len(c.cfg.IgnoreTopics))
	for _, topic := range c.cfg.IgnoreTopics {
		ignoredTopics[topic] = struct{}{}
	}
	subs := make([]paho.SubscribeOptions, 0, len(c.cfg.Points)+len(c.cfg.Subscriptions))
	for _, p := range c.cfg.Points {
		topicMap[p.Topic] = p
		if len(c.cfg.Subscriptions) == 0 {
			subs = append(subs, paho.SubscribeOptions{Topic: p.Topic, QoS: 1})
		}
	}
	for _, sub := range c.cfg.Subscriptions {
		subs = append(subs, paho.SubscribeOptions{Topic: sub.Filter, QoS: sub.QoS})
	}

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
		go c.handleWrite(ctx, cm.Load(), topicMap, msg)
	})
	if err != nil {
		slog.Error("mqtt: write handler subscribe failed", "err", err)
		return
	}
	defer sub.Unsubscribe()

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
					p, ok := topicMap[pr.Packet.Topic]
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
		go c.runFreshnessFloor(ctx, subject, topicMap)
	}

	<-mgr.Done()
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

func (c *Connector) handleWrite(ctx context.Context, cm *autopaho.ConnectionManager, topicMap map[string]PointConfig, msg *nats.Msg) {
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

	p, ok := topicMap[req.LocalID]
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
		QoS:     1,
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
