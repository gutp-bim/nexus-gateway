// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"context"
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

// Config holds all settings for one MQTT connector instance.
type Config struct {
	ConnectorID   string
	BrokerURL     string // e.g. "mqtt://localhost:1883"
	ClientID      string
	Username      string
	Password      []byte
	KeepAlive     uint16
	SessionExpiry uint32 // seconds; 0 = session ends on disconnect
	Points        []PointConfig
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
	dedup     *sdk.CommandDedup
}

func New(cfg Config, nc *nats.Conn, js jetstream.JetStream) *Connector {
	return &Connector{
		cfg:   cfg,
		nc:    nc,
		js:    js,
		ready: make(chan struct{}),
		dedup: sdk.NewCommandDedup(1000),
	}
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
	subs := make([]paho.SubscribeOptions, 0, len(c.cfg.Points))
	for _, p := range c.cfg.Points {
		topicMap[p.Topic] = p
		subs = append(subs, paho.SubscribeOptions{Topic: p.Topic, QoS: 1})
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

	mgr, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{brokerURL},
		KeepAlive:                     c.cfg.KeepAlive,
		CleanStartOnInitialConnection: false,
		SessionExpiryInterval:         c.cfg.SessionExpiry,
		ConnectRetryDelay:             5 * time.Second,
		ConnectUsername:               c.cfg.Username,
		ConnectPassword:               c.cfg.Password,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			if len(subs) > 0 {
				if _, err := cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: subs}); err != nil {
					slog.Error("mqtt: subscribe failed", "err", err)
					return
				}
			}
			// Signal that the first subscription is ready (subsequent reconnects are silently ignored).
			c.readyOnce.Do(func() { close(c.ready) })
		},
		ClientConfig: paho.ClientConfig{
			ClientID: c.cfg.ClientID,
			// Manual acknowledgment: PUBACK is sent only after the event lands in JetStream,
			// preventing data loss when NATS is temporarily unavailable (QoS 1 guarantee).
			EnableManualAcknowledgment: true,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					p, ok := topicMap[pr.Packet.Topic]
					if !ok {
						// Unknown topic: ack immediately to avoid infinite broker retry.
						_ = pr.Client.Ack(pr.Packet)
						return true, nil
					}
					value, ok := extractValue(pr.Packet.Payload)
					if !ok {
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
						Value:       value,
						Unit:        p.Unit,
						Quality:     "Good",
						Timestamp:   time.Now().UTC().Format(time.RFC3339),
					}
					data, err := json.Marshal(evt)
					if err != nil {
						_ = pr.Client.Ack(pr.Packet)
						return true, nil
					}
					if _, err := c.js.Publish(ctx, subject, data); err != nil {
						slog.Warn("mqtt: nats publish failed — withholding PUBACK for QoS 1 retry", "err", err)
						// Do not ack: broker will redeliver when NATS is available again.
						return true, nil
					}
					_ = pr.Client.Ack(pr.Packet)
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

	<-mgr.Done()
}

func (c *Connector) handleWrite(ctx context.Context, cm *autopaho.ConnectionManager, topicMap map[string]PointConfig, msg *nats.Msg) {
	var req sdk.WriteRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil {
		respond(msg, WriteReply{false, "bad_request"})
		return
	}

	// The connection may not be established yet (a command can arrive between the
	// NATS subscribe and cm.Store). Fail fast rather than dereference a nil manager.
	if cm == nil {
		respond(msg, WriteReply{false, "not_connected"})
		return
	}

	// Reserve the slot via CommandDedup. nil-sentinel = in-flight; non-nil = cached.
	proceed, cached := c.dedup.TryReserve(req.ControlID)
	if !proceed {
		if cached == nil {
			// Another goroutine is in-flight; dispatcher will retry.
			respond(msg, WriteReply{false, "in_flight"})
		} else {
			respond(msg, *cached)
		}
		return
	}

	p, ok := topicMap[req.LocalID]
	if !ok || !p.Writable || p.CommandTopic == "" {
		reply := WriteReply{false, "not_writable"}
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
		reply = WriteReply{false, "device_error: " + err.Error()}
	} else {
		reply = WriteReply{true, "ok"}
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

// extractValue extracts a float64 from a raw MQTT payload.
// Supports: plain number ("22.5", "42"), JSON object with a "value" key ({"value": 22.5}).
func extractValue(payload []byte) (float64, bool) {
	s := strings.TrimSpace(string(payload))
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v, true
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return 0, false
	}
	for _, key := range []string{"value", "Value", "v"} {
		if v, ok := obj[key]; ok {
			switch n := v.(type) {
			case float64:
				return n, true
			case string:
				if f, err := strconv.ParseFloat(n, 64); err == nil {
					return f, true
				}
			}
		}
	}
	return 0, false
}
