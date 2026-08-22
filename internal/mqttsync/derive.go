// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package mqttsync derives the MQTT Connector subscription state implied by
// the Building OS Point List and drives a live convergence of a running
// connector to it — the gateway (cmd/gateway) side of #119. See
// docs/adr/0008 for the design.
package mqttsync

import (
	"nexus-gateway/connector/sdk"
	"nexus-gateway/internal/pointlist"
)

// Derive extracts the MQTT subscriptions the Building OS Point List implies
// for one connector: every synced entry with Protocol=="mqtt" and a matching
// ConnectorID becomes a subscription on its LocalID — Building OS's wire
// convention for MQTT is that localId IS the topic
// (docs/architecture/oss-gateway-pointlist-sync.md in gutp-building-os-ri).
//
// Building OS's Point model carries neither QoS nor a distinct write topic
// for MQTT (confirmed by inspecting GatewayPointDto/NativeAddressingDto,
// which is hardcoded to BACnet-only addressing) — docs/adr/0008 records the
// resulting policy: subQoS/cmdQoS apply uniformly across the connector, and a
// writable point's command topic is the same string as its subscribe topic
// (the connector guards against the resulting self-publish loop with MQTT5
// No Local — see connector/mqtt).
func Derive(entries []pointlist.Entry, connectorID string, subQoS, cmdQoS byte) []sdk.SubscriptionSpec {
	var specs []sdk.SubscriptionSpec
	for _, e := range entries {
		if e.Protocol != "mqtt" || e.ConnectorID != connectorID || e.LocalID == "" {
			continue
		}
		spec := sdk.SubscriptionSpec{
			Topic:     e.LocalID,
			QoS:       subQoS,
			Writable:  e.Writable,
			DeviceRef: e.DeviceRef,
			Unit:      e.Unit,
		}
		if e.Writable {
			spec.CommandTopic = e.LocalID
			spec.CommandQoS = cmdQoS
		}
		specs = append(specs, spec)
	}
	return specs
}
