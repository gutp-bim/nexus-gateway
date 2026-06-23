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
