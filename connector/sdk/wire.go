// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package sdk provides shared wire types and utilities for Connector↔Gateway
// internal protocol (ADR-0005). Connectors carry only protocol-specific code;
// the publish-with-ack-window and command-dedup mechanics live here.
package sdk

// WriteRequest is the JSON payload sent by the gateway to a connector's
// write handler over NATS request-reply (subject: cmd.<protocol>.<connectorID>).
type WriteRequest struct {
	ControlID string  `json:"control_id"`
	LocalID   string  `json:"local_id"`
	DeviceRef string  `json:"device_ref"`
	Value     float64 `json:"value"`
	Priority  int32   `json:"priority"`
}

// WriteReply is the JSON payload returned by a connector write handler.
// It is the authoritative definition; dispatch.ConnectorReply is an alias.
type WriteReply struct {
	Success  bool   `json:"success"`
	Response string `json:"response"`
}

// SubscriptionSpec describes one MQTT topic a connector should subscribe to,
// and (for a writable point) the topic/QoS used to publish write commands.
// It is the wire shape shared by internal/mqttsync (gateway side, desired
// state derived from the Building OS Point List) and connector/mqtt (applies
// it to the live broker session) — see docs/adr/0008.
type SubscriptionSpec struct {
	Topic           string `json:"topic"`
	QoS             byte   `json:"qos"` // Subscribe QoS
	Writable        bool   `json:"writable,omitempty"`
	CommandTopic    string `json:"command_topic,omitempty"`
	CommandQoS      byte   `json:"command_qos,omitempty"` // Publish QoS for writes; independent of QoS above
	DeviceRef       string `json:"device_ref,omitempty"`
	Unit            string `json:"unit,omitempty"`
	PayloadTemplate string `json:"payload_template,omitempty"`
}

// SubscriptionApplyRequest asks a running MQTT connector to converge its live
// broker subscriptions to Points, sent over NATS request-reply
// (subject: cfg.mqtt.<connectorID>.apply).
type SubscriptionApplyRequest struct {
	Revision string             `json:"revision"` // Building OS ETag this set was derived from
	Points   []SubscriptionSpec `json:"points"`
}

// SubscriptionApplyReply reports the outcome of a SubscriptionApplyRequest.
// Applied is false — and AppliedRevision/Subscriptions unchanged from before
// the request — when any Subscribe failed; the connector never partially
// commits (docs/adr/0008: previous subscriptions are retained on failure).
type SubscriptionApplyReply struct {
	Applied         bool     `json:"applied"`
	AppliedRevision string   `json:"applied_revision"`
	SubscribedCount int      `json:"subscribed_count"`
	Added           []string `json:"added,omitempty"`
	Changed         []string `json:"changed,omitempty"`
	Removed         []string `json:"removed,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

// SubscriptionStatusReply reports a connector's currently applied
// subscription state, sent over NATS request-reply
// (subject: cfg.mqtt.<connectorID>.status).
type SubscriptionStatusReply struct {
	AppliedRevision string             `json:"applied_revision"`
	Subscriptions   []SubscriptionSpec `json:"subscriptions"`
}
