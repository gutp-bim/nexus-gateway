// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"fmt"
	"time"
)

// Health status values. HTTP stays 200 for both — degraded is a readiness signal
// for dashboards/alerting, not a liveness failure (#45).
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
)

// ComponentHealth is one named subsystem's contribution to overall health.
type ComponentHealth struct {
	Name   string `json:"name"`
	Status string `json:"status"`           // "ok" | "degraded"
	Reason string `json:"reason,omitempty"` // why it is degraded (omitted when ok)
}

// Thresholds tunes the degradation rules so operators can adapt them without a
// rebuild. Zero values fall back to DefaultThresholds in Evaluate.
type Thresholds struct {
	// CheckpointStale: the uplink is degraded when a non-empty backlog has gone
	// un-checkpointed for longer than this. Only accrues while frames are pending,
	// so a quiet gateway never flaps.
	CheckpointStale time.Duration
	// NearCapacityFrac: the buffer is degraded when depth/capacity exceeds this.
	NearCapacityFrac float64
}

// DefaultThresholds are conservative production defaults.
var DefaultThresholds = Thresholds{
	CheckpointStale:  60 * time.Second,
	NearCapacityFrac: 0.9,
}

// HealthInputs is the point-in-time component snapshot the evaluator consumes.
// It carries no live handles and does no I/O, so Evaluate is a pure, table-testable
// function. Has* flags let a minimally-configured server omit a component rather
// than report it as failed.
type HealthInputs struct {
	NatsConnected   bool
	UplinkConnected bool

	HasBuffer          bool
	BufferDepth        int64
	BufferCapacity     int
	WriteErrors        int64
	LastCheckpointUnix int64

	HasPointList bool
	PointCount   int

	Connectors []ConnectorHealth

	Now time.Time
}

// HealthReport is the evaluated health document: an overall status plus the
// per-component breakdown that explains it.
type HealthReport struct {
	Status     string            `json:"status"`
	Components []ComponentHealth `json:"components"`
}

// Evaluate turns a component snapshot into a health document (#45). Overall status
// is degraded if any evaluated component is degraded, else ok. It never panics and
// does no I/O.
func Evaluate(in HealthInputs, cfg Thresholds) HealthReport {
	if cfg.CheckpointStale <= 0 {
		cfg.CheckpointStale = DefaultThresholds.CheckpointStale
	}
	if cfg.NearCapacityFrac <= 0 {
		cfg.NearCapacityFrac = DefaultThresholds.NearCapacityFrac
	}

	comps := []ComponentHealth{evalNats(in)}
	if in.HasBuffer {
		comps = append(comps, evalUplink(in, cfg), evalBuffer(in, cfg))
	}
	if in.HasPointList {
		comps = append(comps, evalPointList(in))
	}
	comps = append(comps, evalConnectors(in))

	status := StatusOK
	for _, c := range comps {
		if c.Status == StatusDegraded {
			status = StatusDegraded
			break
		}
	}
	return HealthReport{Status: status, Components: comps}
}

func ok(name string) ComponentHealth { return ComponentHealth{Name: name, Status: StatusOK} }

func degraded(name, reason string) ComponentHealth {
	return ComponentHealth{Name: name, Status: StatusDegraded, Reason: reason}
}

func evalNats(in HealthInputs) ComponentHealth {
	if !in.NatsConnected {
		return degraded("nats", "NATS broker connection is down")
	}
	return ok("nats")
}

// evalUplink degrades only on a stale-with-pending-frames checkpoint: a non-empty
// backlog that has not been acked within CheckpointStale. An empty backlog (quiet
// gateway) is always fresh, so it never flaps.
func evalUplink(in HealthInputs, cfg Thresholds) ComponentHealth {
	if in.BufferDepth <= 0 {
		return ok("uplink")
	}
	age := time.Duration(in.Now.Unix()-in.LastCheckpointUnix) * time.Second
	if age > cfg.CheckpointStale {
		return degraded("uplink", fmt.Sprintf("%d frames pending; last checkpoint %s ago (> %s)",
			in.BufferDepth, age.Round(time.Second), cfg.CheckpointStale))
	}
	return ok("uplink")
}

func evalBuffer(in HealthInputs, cfg Thresholds) ComponentHealth {
	if in.WriteErrors > 0 {
		return degraded("buffer", fmt.Sprintf("%d buffer write error(s) — frames failed to persist", in.WriteErrors))
	}
	if in.BufferCapacity > 0 {
		frac := float64(in.BufferDepth) / float64(in.BufferCapacity)
		if frac > cfg.NearCapacityFrac {
			return degraded("buffer", fmt.Sprintf("buffer %.0f%% full (%d/%d) — approaching drop-oldest",
				frac*100, in.BufferDepth, in.BufferCapacity))
		}
	}
	return ok("buffer")
}

func evalPointList(in HealthInputs) ComponentHealth {
	if in.PointCount == 0 {
		return degraded("pointlist", "Point List is empty — not synced or no points configured; telemetry cannot resolve")
	}
	return ok("pointlist")
}

func evalConnectors(in HealthInputs) ComponentHealth {
	var down []string
	for _, c := range in.Connectors {
		if !c.Running {
			down = append(down, c.ID)
		}
	}
	if len(down) > 0 {
		return degraded("connectors", fmt.Sprintf("connector(s) not running: %v", down))
	}
	return ok("connectors")
}
