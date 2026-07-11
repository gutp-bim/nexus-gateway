// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"runtime"
	"runtime/metrics"
	"sync"
	"time"
)

// ConnectorHealth is the liveness status of one connector.
type ConnectorHealth struct {
	ID          string
	Image       string
	PrevImage   string // previous digest-pinned image, set if rollback is available (ADR-0006)
	ContainerID string
	Running     bool
}

// GatewayHealth is a point-in-time health snapshot.
type GatewayHealth struct {
	// Status is "ok" when the Admin API is serving; the Admin API sets it on the
	// /health response. The container healthcheck greps for `"status":"ok"`.
	Status string `json:"status,omitempty"`
	// Version is the gateway build version, surfaced on /health for operators.
	// Set by the Admin API from the single-source version package (#22).
	Version       string `json:"version,omitempty"`
	UptimeSeconds float64
	GoRoutines    int
	MemAllocMB    float64
	CPUPercent    float64
	DiskUsedMB    float64
	DiskTotalMB   float64
	Connectors    []ConnectorHealth
}

// GatewayStats holds the host-level health of the gateway process itself,
// independent of any connector. It is the return shape of the GatewayMetrics seam.
type GatewayStats struct {
	UptimeSeconds float64
	GoRoutines    int
	MemAllocMB    float64
	CPUPercent    float64
	DiskUsedMB    float64
	DiskTotalMB   float64
}

// GatewayMetrics samples host-level gateway stats. It touches neither the Registry
// nor the container runtime, so it can be exercised in isolation.
type GatewayMetrics interface {
	Sample() GatewayStats
}

// ConnectorProber resolves the live running state of connectors against the
// container runtime, overriding the registry's possibly-stale Running flag.
type ConnectorProber interface {
	Probe(ctx context.Context, statuses []ConnectorStatus) []ConnectorHealth
}

// HealthMonitor produces health snapshots by composing a GatewayMetrics seam
// (host stats) with a ConnectorProber seam (per-connector liveness).
type HealthMonitor struct {
	registry *Registry
	metrics  GatewayMetrics
	prober   ConnectorProber
}

func NewHealthMonitor(docker ContainerClient, registry *Registry) *HealthMonitor {
	return &HealthMonitor{
		registry: registry,
		metrics:  NewGatewayMetrics(),
		prober:   NewConnectorProber(docker),
	}
}

// Snapshot returns a point-in-time health snapshot. The ctx is propagated to
// the prober's container-runtime calls so callers can enforce deadlines.
func (h *HealthMonitor) Snapshot(ctx context.Context) GatewayHealth {
	s := h.metrics.Sample()
	return GatewayHealth{
		UptimeSeconds: s.UptimeSeconds,
		GoRoutines:    s.GoRoutines,
		MemAllocMB:    s.MemAllocMB,
		CPUPercent:    s.CPUPercent,
		DiskUsedMB:    s.DiskUsedMB,
		DiskTotalMB:   s.DiskTotalMB,
		Connectors:    h.prober.Probe(ctx, h.registry.List()),
	}
}

// ── GatewayMetrics: default host-stats implementation ─────────────────────────

type runtimeMetrics struct {
	startTime time.Time // when this monitor was created; per-instance, not package-global

	// Disk stats are sampled at most once per diskTTL and cached: a `statfs` can
	// block on a slow/stale mount, and /health (a liveness probe) is hit often.
	diskFn  func() (usedMB, totalMB float64)
	diskTTL time.Duration
	diskMu  sync.Mutex
	dUsed   float64
	dTotal  float64
	dAt     time.Time
	dInit   bool

	// CPU% is a delta between two runtime/metrics cpu-seconds samples, so like
	// disk it is cached and recomputed at most once per cpuTTL — a sub-second
	// window would produce a noisy, meaningless ratio, and /health is polled often.
	cpuTTL    time.Duration
	cpuMu     sync.Mutex
	cpuAt     time.Time
	cpuPct    float64
	cpuInit   bool
	lastTotal float64 // /cpu/classes/total:cpu-seconds at last recompute
	lastIdle  float64 // /cpu/classes/idle:cpu-seconds at last recompute
}

// NewGatewayMetrics returns the default GatewayMetrics backed by the Go runtime
// and the host filesystem. Uptime is measured from the moment of construction.
func NewGatewayMetrics() GatewayMetrics {
	return newRuntimeMetrics(diskStatsMB, 15*time.Second)
}

func newRuntimeMetrics(diskFn func() (usedMB, totalMB float64), diskTTL time.Duration) *runtimeMetrics {
	return &runtimeMetrics{startTime: time.Now(), diskFn: diskFn, diskTTL: diskTTL, cpuTTL: time.Second}
}

// cpuPercent returns the share of available CPU capacity (GOMAXPROCS-normalized)
// the Go runtime consumed since the last recompute, as a percentage. It is a
// delta of runtime/metrics cpu-seconds classes, recomputed at most once per
// cpuTTL; the first call establishes the baseline and returns 0. If the CPU
// classes are unavailable on this runtime, it returns 0.
func (m *runtimeMetrics) cpuPercent() float64 {
	m.cpuMu.Lock()
	defer m.cpuMu.Unlock()

	samples := []metrics.Sample{
		{Name: "/cpu/classes/total:cpu-seconds"},
		{Name: "/cpu/classes/idle:cpu-seconds"},
	}
	metrics.Read(samples)
	if samples[0].Value.Kind() != metrics.KindFloat64 || samples[1].Value.Kind() != metrics.KindFloat64 {
		return 0 // CPU classes not supported on this runtime
	}
	total, idle := samples[0].Value.Float64(), samples[1].Value.Float64()

	if !m.cpuInit {
		m.lastTotal, m.lastIdle, m.cpuAt, m.cpuInit = total, idle, time.Now(), true
		return 0 // no delta yet
	}
	if time.Since(m.cpuAt) < m.cpuTTL {
		return m.cpuPct // reuse cached value within the window
	}

	dTotal, dIdle := total-m.lastTotal, idle-m.lastIdle
	pct := 0.0
	if dTotal > 0 {
		busy := dTotal - dIdle
		if busy < 0 {
			busy = 0
		}
		pct = busy / dTotal * 100
	}
	m.lastTotal, m.lastIdle, m.cpuAt, m.cpuPct = total, idle, time.Now(), pct
	return pct
}

// disk returns cached disk stats, refreshing via diskFn at most once per diskTTL.
func (m *runtimeMetrics) disk() (usedMB, totalMB float64) {
	m.diskMu.Lock()
	defer m.diskMu.Unlock()
	if !m.dInit || time.Since(m.dAt) >= m.diskTTL {
		m.dUsed, m.dTotal = m.diskFn()
		m.dAt = time.Now()
		m.dInit = true
	}
	return m.dUsed, m.dTotal
}

func (m *runtimeMetrics) Sample() GatewayStats {
	// Use runtime/metrics instead of runtime.ReadMemStats to avoid a stop-the-world pause.
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(samples)
	var allocMB float64
	if samples[0].Value.Kind() == metrics.KindUint64 {
		allocMB = float64(samples[0].Value.Uint64()) / 1024 / 1024
	}
	diskUsed, diskTotal := m.disk()
	return GatewayStats{
		UptimeSeconds: time.Since(m.startTime).Seconds(),
		GoRoutines:    runtime.NumGoroutine(),
		MemAllocMB:    allocMB,
		CPUPercent:    m.cpuPercent(),
		DiskUsedMB:    diskUsed,
		DiskTotalMB:   diskTotal,
	}
}

// ── ConnectorProber: default container-runtime implementation ─────────────────

type dockerProber struct {
	docker ContainerClient
}

// NewConnectorProber returns the default ConnectorProber that verifies liveness
// by inspecting each connector's container in the daemon. A nil docker client
// makes Probe trust the registry's Running flag verbatim (no override).
func NewConnectorProber(docker ContainerClient) ConnectorProber {
	return &dockerProber{docker: docker}
}

func (p *dockerProber) Probe(ctx context.Context, statuses []ConnectorStatus) []ConnectorHealth {
	connectors := make([]ConnectorHealth, len(statuses))
	var wg sync.WaitGroup
	for i, status := range statuses {
		connectors[i] = ConnectorHealth{
			ID:          status.Spec.ID,
			Image:       status.Spec.Image,
			PrevImage:   status.Spec.PrevImage,
			ContainerID: status.ContainerID,
			Running:     status.Running,
		}
		if status.ContainerID != "" && p.docker != nil {
			wg.Add(1)
			go func(idx int, containerID string) {
				defer wg.Done()
				if resp, err := p.docker.ContainerInspect(ctx, containerID); err == nil {
					connectors[idx].Running = resp.ContainerJSONBase != nil && resp.State != nil && resp.State.Running
				} else {
					connectors[idx].Running = false
				}
			}(i, status.ContainerID)
		}
	}
	wg.Wait()
	return connectors
}
