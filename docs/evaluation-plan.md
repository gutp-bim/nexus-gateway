# Evaluation Plan — nexus-gateway

*English / [日本語](evaluation-plan.ja.md)* — top: [README](../README.md)

This document defines the experiment design, metric definitions, workload tables,
and expected findings for the empirical evaluation of nexus-gateway. Evaluations
E1–E5 are mandatory for the paper; E6–E7 are optional extensions.

The corresponding test scaffolds are under `test/e2e/eval_e*.go` (build tag `e2e`).

---

## Setup

### Software stack

| Component | Version / Image |
|-----------|----------------|
| nexus-gateway | this repo (master) |
| NATS JetStream | `nats:2.10-alpine` |
| BACnet simulator | `../bacnet-sim-gateway` |
| OPC-UA simulator | `../opcua-sim-gateway` |
| Building OS mock | `../gutp-building-os-ri` (GatewayIngress gRPC stub) |
| Go | 1.25 |
| SQLite | bundled via `modernc.org/sqlite` |

### Hardware (reference)

All evaluations use a single host with:

- CPU: 4 cores (Intel/AMD x86-64 or ARM64)
- RAM: 8 GiB
- Disk: SSD, ≥10 GiB free
- Network: loopback (all components co-located); for WAN latency tests add a `tc netem` rule

### Environment variables for each evaluation

See the `Environment:` section in each `test/e2e/eval_e*.go` file for the full
list. The common variables are:

| Variable | Default | Purpose |
|----------|---------|---------|
| `E2E_NATS_URL` | — (required) | NATS URL for JetStream |
| `E2E_ADMIN_URL` | `http://localhost:18080` | nexus-gateway Admin API |
| `E2E_BOS_API_URL` | — | Building OS REST API (SoS tests) |

### Running a single evaluation

```bash
# Start the integration stack (OPC-UA profile):
docker compose -f docker-compose.yml -f docker-compose.integration.yml \
  --profile opcua up -d

# Run one evaluation (e.g. E1 throughput with 1000 points, 1 s interval):
E2E_NATS_URL=nats://localhost:14222 \
E2E_E1_POINTS=1000 \
E2E_E1_INTERVALS=1 \
E2E_E1_WINDOW=30 \
go test -v -tags e2e -run TestE1 ./test/e2e/

# All mandatory evaluations (long; ~3 hours for full matrix):
E2E_NATS_URL=nats://localhost:14222 \
go test -v -tags e2e -timeout 4h ./test/e2e/
```

---

## E1 — Telemetry Throughput Scaling

**Hypothesis**: nexus-gateway sustains the target throughput (events/s) across the
full point-count/interval matrix without accumulating unbounded NATS backlog or
exhausting SQLite ring buffer capacity.

### Workload matrix

| Points | Interval | Target events/s | Target frames/s |
|--------|----------|----------------|-----------------|
| 100    | 1 s      | 100            | 100             |
| 100    | 10 s     | 10             | 10              |
| 100    | 60 s     | 1.7            | 1.7             |
| 1 000  | 1 s      | 1 000          | 1 000           |
| 1 000  | 10 s     | 100            | 100             |
| 5 000  | 1 s      | 5 000          | 5 000           |
| 10 000 | 60 s     | 167            | 167             |

### Metrics

| Metric | Unit | Source |
|--------|------|--------|
| `events_per_s` | ev/s | JetStream message count / window |
| `frames_per_s` | fr/s | `storefwd_sent_total` delta / window |
| `nats_lag` | messages | JetStream stream state at window end |
| `sqlite_depth` | rows | `/metrics` `storefwd_buffer_depth` |
| `cpu_delta_s` | s | `/metrics` `process_cpu_seconds_total` delta |
| `mem_mib` | MiB | `/metrics` `process_resident_memory_bytes` |

### Expected findings

- events/s tracks the theoretical rate (points ÷ interval) within ±5%.
- NATS lag stays near zero at ≤1 000 points/1 s; rises at 5 000+/1 s.
- SQLite depth stays near zero for moderate rates; climbs during backpressure.
- CPU stays below 1 core at 1 000 points/1 s; scales roughly linearly.

### Test scaffold

`test/e2e/eval_e1_throughput_test.go` — `TestE1_ThroughputScaling`

---

## E2 — End-to-End Latency

**Hypothesis**: the pipeline adds negligible latency over the underlying protocol
poll cycle; the dominant contributor is the connector's protocol read interval.

### Stage breakdown

```
Device read → T0 (device timestamp in Common Event)
NATS publish → T1 (JetStream message arrival)
Normalizer   → T2 (TelemetryFrame emitted)
S&F write    → T3 (SQLite row inserted)
gRPC send    → T4 (frame dequeued and sent)
BOS receive  → T5 (GatewayIngress.StreamTelemetry receive)
```

### Metrics

| Stage | Proxy measurement | Unit |
|-------|------------------|------|
| T0 → T1 (device → NATS) | wall clock at JetStream consumption − `event.Timestamp` | ms |
| T1 → T3 (NATS → S&F) | not yet directly observable; target < 50 ms | ms |
| T3 → T4 (S&F → gRPC send) | not yet directly observable; target < 100 ms | ms |
| T4 → T5 (gRPC → BOS) | mock ingress receive timestamp − S&F send timestamp | ms |

Report p50 / p95 / p99 / max for each measured stage.

### Expected findings

- p99 device→NATS below one polling interval + 200 ms.
- Total pipeline latency (T0→T5) p99 < 2 × polling interval under normal load.
- Latency degrades gracefully (not catastrophically) at 5 000+ points/1 s.

### Test scaffold

`test/e2e/eval_e2_latency_test.go` — `TestE2_EndToEndLatency`

---

## E3 — Store-and-Forward Recovery

**Hypothesis**: the bounded SQLite ring buffer sustains normal telemetry through
outages up to 10 minutes without data loss; longer outages drop the oldest entries
in a controlled way; all buffered data drains promptly when the uplink recovers.

### Outage scenarios

| Outage | Points | Interval | Expected buffered | Expected drops |
|--------|--------|----------|------------------|----------------|
| 1 min  | 100    | 1 s      | ≤ 6 000          | 0              |
| 10 min | 100    | 1 s      | ≤ 60 000         | 0              |
| 30 min | 100    | 1 s      | ≤ 180 000        | 0 (if cap ≥ 180 000) |
| 60 min | 100    | 1 s      | ≤ 360 000        | cap-dependent  |

Ring buffer capacity is set via `STOREFWD_MAX_ROWS` (default: 1 000 000 rows).

### Metrics

| Metric | Unit | Source |
|--------|------|--------|
| `max_buffer_depth` | rows | `/metrics` during outage |
| `dropped_total` | rows | `/metrics` `storefwd_dropped_total` |
| `recovery_time_s` | s | wall clock from uplink-restore to depth=0 |
| `sent_after_recovery` | frames | `storefwd_sent_total` delta |

### Expected findings

- Zero drops for outages ≤ buffer capacity / point_rate.
- Recovery time scales linearly with buffered row count and uplink bandwidth.
- Drop-oldest semantics: newest data reaches Building OS first after recovery.

### Test scaffold

`test/e2e/eval_e3_storefwd_test.go` — `TestE3_StoreFwdRecovery`

---

## E4 — Point List Drift and Mapping Update

**Hypothesis**: the gateway converges to a new Point List within one poll interval
(default 10 min; forced by the `/point-list-refresh` Admin API) and correctly
resolves/re-routes events after mapping changes.

### Sub-scenarios

| Scenario | Input | Expected output |
|----------|-------|----------------|
| `unknown` | `local_id` absent from Point List | `normalizer_unresolved_total` +1 per event; no TelemetryFrame |
| `remap`   | `local_id` mapped to a new `point_id` | sync detected within poll interval; subsequent frames use new `point_id` |
| `unit`    | unit changed for an existing `local_id` | post-sync frames carry new canonical unit |

### Metrics

| Metric | Unit | Source |
|--------|------|--------|
| `unresolved_ratio` | fraction | `normalizer_unresolved_total` / total events |
| `sync_time_s` | s | refresh trigger → first frame with new mapping |
| `accepted_after_remap` | frames | `storefwd_sent_total` delta post-sync |

### Expected findings

- `unknown` ratio equals the fraction of points missing from the Point List.
- `remap` sync time < 5 s when forced via Admin API; < poll interval otherwise.
- `unit` change takes effect at the same time as `remap`.

### Test scaffold

`test/e2e/eval_e4_pointlist_drift_test.go` — `TestE4_PointListDrift`

---

## E5 — Control Command Safety

**Hypothesis**: the real-time-or-fail control path (ADR-0004) never delivers a
stale, duplicate, or type-erroneous command to physical equipment, and fails
immediately (not after a buffered delay) when the uplink is unavailable.

### Sub-scenarios

| Scenario | Stimulus | Expected response |
|----------|----------|------------------|
| `stale_deadline` | command with `expired_at` in the past | synchronous error reply; no write |
| `typed_failure`  | value of wrong type (string instead of float) | typed error reply within timeout |
| `idempotent`     | same `control_id` sent twice | second reply = cached first; no double-write |
| `no_buffer`      | uplink down; control command sent | immediate failure (NATS timeout); no queued write |

### Metrics

| Metric | Unit |
|--------|------|
| `passed` | bool per scenario |
| `latency_ms` | response latency (ms) |

### Expected findings

- All four scenarios pass.
- Stale and typed-failure responses arrive within the NATS request-reply timeout (< 10 s).
- No equipment write occurs for stale, typed-failure, or no-buffer scenarios.

### Test scaffold

`test/e2e/eval_e5_control_safety_test.go` — `TestE5_ControlCommandSafety`

---

## E6 — Connector Update and Rollback (Optional)

**Hypothesis**: a connector upgrade via the Connector Catalog (cosign-signed OCI,
digest-pinned, stop→replace→health-check→rollback) completes with a telemetry gap
shorter than two polling intervals and rolls back automatically on health-check failure.

### Metrics

| Metric | Unit | Source |
|--------|------|--------|
| `detection_time_s` | s | catalog poll → Admin API shows new image pending |
| `pull_verify_time_s` | s | detection → cosign OK + image pulled |
| `downtime_s` | s | connector stopped → health-check green |
| `telemetry_gap_s` | s | last event before stop → first event after start |
| `rollback_triggered` | bool | health-check failed → old image restarted |

### Test scaffold

`test/e2e/eval_e6_connector_lifecycle_test.go` — `TestE6_ConnectorUpdateRollback`

**Prerequisite**: push a cosign-signed image to GHCR and register it in the Connector Catalog.

---

## E7 — Comparison with Adjacent Systems (Optional)

This evaluation is qualitative. The paper compares nexus-gateway to:
EdgeX Foundry, ThingsBoard IoT Gateway, EMQX Neuron, Azure IoT Edge, Eclipse Kura.

Comparison axes:

| Axis | nexus-gateway | Others |
|------|--------------|--------|
| Registry ownership | Building OS Digital Twin only | Own registry (EdgeX Core Metadata, etc.) |
| Command path | gRPC GatewayEgress → core NATS request-reply | Varies (REST, MQTT, etc.) |
| Connector isolation | OCI container per protocol | Varies |
| Point List source of truth | Building OS (diff-synced copy at edge) | Local registry |
| Lock-in vector | Connector Catalog / gRPC contract | Cloud control plane or platform |

No automated test — assessment is based on published documentation and prototype
comparisons described in [docs/background.md](background.md).

---

## Output Format

Each evaluation test emits a CSV block to `t.Log`. Extract it with:

```bash
go test -v -tags e2e -run TestE1 ./test/e2e/ 2>&1 \
  | grep -A 9999 'E1 results (CSV'
```

Paste the CSV directly into the paper's table (Numbers / Excel / LaTeX `pgfplotstable`).

---

## Paper Reference

Target venue: 建築情報学会 (Architectural Informatics Society of Japan).
Submission deadline: TBD.

The evaluations correspond to sections in the paper as follows:

| Evaluation | Paper section |
|------------|--------------|
| E1         | §4.1 Throughput |
| E2         | §4.2 Latency |
| E3         | §4.3 Resilience |
| E4         | §4.4 Semantic Convergence |
| E5         | §4.5 Control Safety |
| E6         | §4.6 Operational Lifecycle (optional) |
| E7         | §5 Comparison |
