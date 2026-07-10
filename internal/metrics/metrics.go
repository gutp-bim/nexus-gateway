// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package metrics holds process-wide counters exposed at the Admin API /metrics
// endpoint. Counters are global (Prometheus-style): producers increment them,
// the exposition handler reads them; neither imports the other.
package metrics

import "sync/atomic"

var (
	normalizerInvalid    atomic.Int64
	normalizerUnresolved atomic.Int64

	// Connectivity gauges (#23). Cross-cutting process state that belongs to a
	// specific connection object (the NATS conn, the Building OS uplink) rather
	// than to the store-and-forward buffer, so it lives here as a global rather
	// than on TelemetrySource. Producers Set on lifecycle transitions; the
	// exposition handler and the telemetry payload read.
	natsConnected   atomic.Bool
	uplinkConnected atomic.Bool
)

// IncNormalizerInvalid counts a Common Event the Normalizer could not parse
// (poison) and terminated.
func IncNormalizerInvalid() { normalizerInvalid.Add(1) }

// IncNormalizerUnresolved counts a Common Event whose local_id is absent from
// the Point List (point-list miss) and was terminated.
func IncNormalizerUnresolved() { normalizerUnresolved.Add(1) }

// NormalizerInvalid returns the current poison count.
func NormalizerInvalid() int64 { return normalizerInvalid.Load() }

// NormalizerUnresolved returns the current point-list-miss count.
func NormalizerUnresolved() int64 { return normalizerUnresolved.Load() }

// SetNatsConnected records the current NATS broker connection state, driven by
// the connection's disconnect/reconnect/closed lifecycle callbacks (#23).
func SetNatsConnected(v bool) { natsConnected.Store(v) }

// NatsConnected reports whether the gateway currently holds a live NATS connection.
func NatsConnected() bool { return natsConnected.Load() }

// SetUplinkConnected records the current Building OS uplink state: true after a
// successful ack-checkpoint, false on a send/checkpoint failure or stream reset (#23).
func SetUplinkConnected(v bool) { uplinkConnected.Store(v) }

// UplinkConnected reports whether the Building OS telemetry uplink is currently healthy.
func UplinkConnected() bool { return uplinkConnected.Load() }

// b2f maps a boolean gauge to its Prometheus 1/0 representation.
func b2f(v bool) int {
	if v {
		return 1
	}
	return 0
}

// NatsConnectedGauge / UplinkConnectedGauge return the 1/0 gauge value for exposition.
func NatsConnectedGauge() int   { return b2f(natsConnected.Load()) }
func UplinkConnectedGauge() int { return b2f(uplinkConnected.Load()) }
