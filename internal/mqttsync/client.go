// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mqttsync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	mqttconn "nexus-gateway/connector/mqtt"
	"nexus-gateway/connector/sdk"
	"nexus-gateway/internal/pointlist"
)

const defaultTimeout = 5 * time.Second

// Snapshotter is the pointlist.SyncedResolver subset Client needs — the
// gateway-side synced Point List snapshot Derive reads from. Satisfied by
// *pointlist.SyncedResolver.
type Snapshotter interface {
	Snapshot() []pointlist.Entry
}

// Config configures per-connector QoS defaults, since Building OS's Point
// List carries no QoS/command-topic fields for MQTT (docs/adr/0008).
type Config struct {
	// DefaultSubscribeQoS/DefaultCommandQoS map connector_id to the QoS
	// applied to every synced point on that connector; a missing entry or a
	// zero value falls back to 1.
	DefaultSubscribeQoS map[string]byte
	DefaultCommandQoS   map[string]byte
	// Timeout bounds each NATS request-reply to a connector; <=0 uses
	// defaultTimeout.
	Timeout time.Duration
}

// Client drives Point-List-sync preview/apply against a live MQTT connector
// over its NATS control plane (cfg.mqtt.<connector_id>.{status,apply}) — the
// gateway-side half of #119 (docs/adr/0008).
type Client struct {
	nc       *nats.Conn
	resolver Snapshotter
	cfg      Config
}

// NewClient builds a Client. resolver is typically the same
// *pointlist.SyncedResolver the Normalizer uses (cmd/gateway wires both to
// the one pointsync.Loop).
func NewClient(nc *nats.Conn, resolver Snapshotter, cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Client{nc: nc, resolver: resolver, cfg: cfg}
}

func (c *Client) subscribeQoS(connectorID string) byte {
	if q, ok := c.cfg.DefaultSubscribeQoS[connectorID]; ok && q != 0 {
		return q
	}
	return 1
}

func (c *Client) commandQoS(connectorID string) byte {
	if q, ok := c.cfg.DefaultCommandQoS[connectorID]; ok && q != 0 {
		return q
	}
	return 1
}

// desired recomputes the Point-List-derived subscription set fresh from the
// current resolver snapshot — Preview and Apply each call this independently
// rather than sharing a cached value, so a request always reflects the
// latest sync (no TOCTOU window between an operator's preview and apply).
func (c *Client) desired(connectorID string) []sdk.SubscriptionSpec {
	return Derive(c.resolver.Snapshot(), connectorID, c.subscribeQoS(connectorID), c.commandQoS(connectorID))
}

// status queries connectorID's currently applied subscription state.
func (c *Client) status(ctx context.Context, connectorID string) (sdk.SubscriptionStatusReply, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	msg, err := c.nc.RequestWithContext(ctx, "cfg.mqtt."+connectorID+".status", nil)
	if err != nil {
		return sdk.SubscriptionStatusReply{}, fmt.Errorf("mqttsync: status request to %s: %w", connectorID, err)
	}
	var status sdk.SubscriptionStatusReply
	if err := json.Unmarshal(msg.Data, &status); err != nil {
		return sdk.SubscriptionStatusReply{}, fmt.Errorf("mqttsync: decode status reply from %s: %w", connectorID, err)
	}
	return status, nil
}

// Preview computes the diff between connectorID's currently applied
// subscriptions and the latest Point-List-derived desired state, without
// changing anything.
func (c *Client) Preview(ctx context.Context, connectorID string) (Diff, error) {
	status, err := c.status(ctx, connectorID)
	if err != nil {
		return Diff{}, err
	}
	desired := c.desired(connectorID)
	d := computeDiff(status.Subscriptions, desired)
	d.CurrentRevision = status.AppliedRevision
	d.TargetRevision = revisionOf(desired)
	return d, nil
}

// Apply validates and pushes the latest Point-List-derived desired state to
// connectorID, which converges its live broker subscriptions to it
// (connector/mqtt.Connector.ApplySubscriptions — atomic, retains the
// previous state on failure per docs/adr/0008).
func (c *Client) Apply(ctx context.Context, connectorID string) (sdk.SubscriptionApplyReply, error) {
	desired := c.desired(connectorID)

	points := make([]mqttconn.PointConfig, len(desired))
	for i, s := range desired {
		points[i] = mqttconn.PointConfig{
			Topic:           s.Topic,
			DeviceRef:       s.DeviceRef,
			Unit:            s.Unit,
			Writable:        s.Writable,
			CommandTopic:    s.CommandTopic,
			PayloadTemplate: s.PayloadTemplate,
			QoS:             s.QoS,
			CommandQoS:      s.CommandQoS,
		}
	}
	if err := mqttconn.ValidatePoints(points); err != nil {
		return sdk.SubscriptionApplyReply{}, fmt.Errorf("mqttsync: invalid Point-List-derived subscriptions for %s: %w", connectorID, err)
	}

	req := sdk.SubscriptionApplyRequest{Revision: revisionOf(desired), Points: desired}
	data, err := json.Marshal(req)
	if err != nil {
		return sdk.SubscriptionApplyReply{}, fmt.Errorf("mqttsync: encode apply request for %s: %w", connectorID, err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	msg, err := c.nc.RequestWithContext(ctx, "cfg.mqtt."+connectorID+".apply", data)
	if err != nil {
		return sdk.SubscriptionApplyReply{}, fmt.Errorf("mqttsync: apply request to %s: %w", connectorID, err)
	}
	var reply sdk.SubscriptionApplyReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return sdk.SubscriptionApplyReply{}, fmt.Errorf("mqttsync: decode apply reply from %s: %w", connectorID, err)
	}
	return reply, nil
}
