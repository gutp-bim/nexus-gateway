// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
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
	BrokerURL     string // e.g. "mqtt://localhost:1883" or "mqtts://broker:8883"
	ClientID      string
	Username      string
	Password      []byte
	KeepAlive     uint16
	SessionExpiry uint32 // seconds; 0 = session ends on disconnect
	Points        []PointConfig

	// TLS material for mqtts:// brokers (#33). Only consulted when the broker URL
	// scheme is a TLS scheme (mqtts/ssl/tls); plain mqtt:// ignores these.
	TLSCAFile             string // PEM CA bundle to verify the broker cert; empty = system roots
	TLSCertFile           string // client certificate for mutual TLS (paired with TLSKeyFile)
	TLSKeyFile            string // client private key for mutual TLS (paired with TLSCertFile)
	TLSInsecureSkipVerify bool   // DEV ONLY: skip broker cert verification

	// FreshnessInterval enables the freshness floor (#34): when > 0, each Point's
	// last-known value is re-published as a Common Event if no broker update
	// arrived within the interval, matching the poll cadence of BACnet/OPC-UA.
	// 0 disables the floor (pure push, prior behaviour).
	FreshnessInterval time.Duration
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
	connected atomic.Bool // broker session state, for the /health probe (#35)
	dedup     *sdk.CommandDedup

	// lkv holds the last-known value per topic (local_id) for the freshness floor
	// (#34), guarded by lkvMu. Populated only from real broker updates, so a Point
	// that never reported is never invented.
	lkvMu sync.Mutex
	lkv   map[string]*lkvState
}

// lkvState is a Point's last-known value and the wall clock of its last emission
// (broker update or freshness re-publish, whichever is most recent).
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

// Healthy reports whether the MQTT broker session is currently up. It backs the
// connector's /health probe (#35): false before the first connect and after a
// server disconnect / client error, true while a session is established.
func (c *Connector) Healthy() bool {
	return c.connected.Load()
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

	// Build TLS config for mqtts:// brokers (#33). Plain mqtt:// leaves it nil.
	var tlsCfg *tls.Config
	if isTLSScheme(brokerURL.Scheme) {
		tlsCfg, err = buildTLSConfig(c.cfg)
		if err != nil {
			slog.Error("mqtt: TLS configuration failed", "err", err)
			return
		}
	}

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
		TlsCfg:                        tlsCfg,
		KeepAlive:                     c.cfg.KeepAlive,
		CleanStartOnInitialConnection: false,
		SessionExpiryInterval:         c.cfg.SessionExpiry,
		ConnectRetryDelay:             5 * time.Second,
		ConnectUsername:               c.cfg.Username,
		ConnectPassword:               c.cfg.Password,
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
			// Broker session is up — reflect it in /health (#35).
			c.connected.Store(true)
			// Signal that the first subscription is ready (subsequent reconnects are silently ignored).
			c.readyOnce.Do(func() { close(c.ready) })
		},
		OnConnectError: func(err error) {
			// A connection attempt failed (broker unreachable): stay/return to not-ok.
			c.connected.Store(false)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: c.cfg.ClientID,
			// Manual acknowledgment: PUBACK is sent only after the event lands in JetStream,
			// preventing data loss when NATS is temporarily unavailable (QoS 1 guarantee).
			EnableManualAcknowledgment: true,
			// Broker session lost — mark unhealthy until autopaho reconnects (#35).
			OnServerDisconnect: func(*paho.Disconnect) { c.connected.Store(false) },
			OnClientError:      func(error) { c.connected.Store(false) },
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
					if !c.publishValue(ctx, subject, p, value, time.Now()) {
						slog.Warn("mqtt: nats publish failed — withholding PUBACK for QoS 1 retry", "topic", pr.Packet.Topic)
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

	// Freshness floor (#34): periodically re-publish the last-known value of any
	// Point idle beyond the interval, so a never-changing broker value does not
	// look perpetually stale to Building OS. Disabled when the interval is 0.
	if c.cfg.FreshnessInterval > 0 {
		go c.runFreshnessFloor(ctx, subject, topicMap)
	}

	<-mgr.Done()
}

// runFreshnessFloor ticks at the freshness interval, re-publishing stale points
// until ctx is cancelled.
func (c *Connector) runFreshnessFloor(ctx context.Context, subject string, topicMap map[string]PointConfig) {
	ticker := time.NewTicker(c.cfg.FreshnessInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.republishStale(ctx, subject, topicMap, time.Now())
		}
	}
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

// isTLSScheme reports whether a broker URL scheme requires TLS.
func isTLSScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "mqtts", "ssl", "tls":
		return true
	}
	return false
}

// buildTLSConfig assembles a *tls.Config from the connector's TLS material for
// mqtts:// brokers (#33). It mirrors internal/transport: a CA bundle overrides the
// system roots, a client cert/key pair enables mutual TLS, and the pair must be
// supplied together. A UI/dev-only skip-verify flag disables verification. The
// returned config is always at least TLS 1.2.
func buildTLSConfig(cfg Config) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.TLSInsecureSkipVerify} //nolint:gosec // skip-verify is an explicit, documented dev-only opt-in

	if cfg.TLSCAFile != "" {
		pem, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("MQTT_TLS_CA_FILE: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("MQTT_TLS_CA_FILE: no valid PEM certificates in %s", cfg.TLSCAFile)
		}
		tc.RootCAs = pool
	}

	// Client cert and key are a pair: one without the other is a misconfiguration.
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		if cfg.TLSCertFile == "" {
			return nil, fmt.Errorf("MQTT_TLS_CERT_FILE is required when MQTT_TLS_KEY_FILE is set")
		}
		return nil, fmt.Errorf("MQTT_TLS_KEY_FILE is required when MQTT_TLS_CERT_FILE is set")
	}
	if cfg.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("MQTT_TLS_CERT_FILE/MQTT_TLS_KEY_FILE: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}

	return tc, nil
}

// publishValue builds and publishes a Common Event for point p with value at ts,
// and records it as the point's last-known value for the freshness floor. Returns
// false if the JetStream publish failed (caller decides whether to ack/retry).
func (c *Connector) publishValue(ctx context.Context, subject string, p PointConfig, value float64, ts time.Time) bool {
	evt := common.Event{
		Protocol:    "mqtt",
		ConnectorID: c.cfg.ConnectorID,
		LocalID:     p.Topic,
		DeviceRef:   p.DeviceRef,
		Value:       value,
		Unit:        p.Unit,
		Quality:     "Good",
		Timestamp:   ts.UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return false
	}
	if _, err := c.js.Publish(ctx, subject, data); err != nil {
		return false
	}
	c.recordValue(p.Topic, value, ts)
	return true
}

// recordValue stores/refreshes a point's last-known value and resets its floor timer.
func (c *Connector) recordValue(topic string, value float64, ts time.Time) {
	c.lkvMu.Lock()
	c.lkv[topic] = &lkvState{value: value, lastEmit: ts}
	c.lkvMu.Unlock()
}

// dueForRepublish returns the topics whose last emission is older than the
// freshness interval at now. Returns nothing when the floor is disabled
// (interval <= 0), and never returns a point that has not reported (absent from lkv).
func (c *Connector) dueForRepublish(now time.Time) []string {
	if c.cfg.FreshnessInterval <= 0 {
		return nil
	}
	c.lkvMu.Lock()
	defer c.lkvMu.Unlock()
	var due []string
	for topic, st := range c.lkv {
		if now.Sub(st.lastEmit) >= c.cfg.FreshnessInterval {
			due = append(due, topic)
		}
	}
	return due
}

// republishStale re-publishes the last-known value of every point idle beyond the
// freshness floor, stamping the event with now (#34).
func (c *Connector) republishStale(ctx context.Context, subject string, topicMap map[string]PointConfig, now time.Time) {
	for _, topic := range c.dueForRepublish(now) {
		p, ok := topicMap[topic]
		if !ok {
			continue
		}
		c.lkvMu.Lock()
		st := c.lkv[topic]
		// Re-verify still due under the re-lock: a broker update landing between
		// selection and here resets lastEmit, so re-publishing would emit a
		// same-value duplicate. Skip it (also guards a future lkv eviction → nil).
		if st == nil || now.Sub(st.lastEmit) < c.cfg.FreshnessInterval {
			c.lkvMu.Unlock()
			continue
		}
		value := st.value
		c.lkvMu.Unlock()
		c.publishValue(ctx, subject, p, value, now)
	}
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
// NaN and Inf are rejected: json.Marshal cannot encode them, so passing them
// through would cause a silent data-loss bug (marshal error → ack without publish).
func extractValue(payload []byte) (float64, bool) {
	s := strings.TrimSpace(string(payload))
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
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
				// JSON numbers are finite by spec; no NaN/Inf check needed here.
				return n, true
			case string:
				if f, err := strconv.ParseFloat(n, 64); err == nil {
					if math.IsNaN(f) || math.IsInf(f, 0) {
						return 0, false
					}
					return f, true
				}
			}
		}
	}
	return 0, false
}
