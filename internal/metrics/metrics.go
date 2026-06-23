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
