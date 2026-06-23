// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package common

// Event is the protocol-tagged message a Connector publishes to NATS JetStream.
// It carries native addressing only (LocalID + DeviceRef) — no canonical PointID.
// Published on subject evt.<protocol>.<connector_id> in the EVENTS stream (ADR-0001, ADR-0005).
type Event struct {
	Protocol    string  `json:"protocol"`
	ConnectorID string  `json:"connector_id"`
	LocalID     string  `json:"local_id"`
	DeviceRef   string  `json:"device_ref"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	Quality     string  `json:"quality"`
	Timestamp   string  `json:"timestamp"`
}
