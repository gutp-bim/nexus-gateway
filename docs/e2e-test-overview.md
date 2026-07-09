# E2E Test Overview

This document describes the full integration and end-to-end test landscape for
nexus-gateway. Tests are grouped by whether they need a live external stack.

---

## Layer 1 — In-process integration tests (always run in CI)

No external services required. Each test starts embedded NATS and uses mock gRPC
servers, so the suite runs in `go test -race ./...` without any stack.

| Test file | Test functions | What it verifies |
|-----------|---------------|-----------------|
| `integration/skeleton_test.go` | `TestE2E_SimConnectorFrameArrivesAtBOS` | Sim connector publishes a Common Event → Normalizer resolves `point_id` → Uplink streams `TelemetryFrame` to mock BOS (tracer bullet) |
| | `TestE2E_NativeAddressingOnlyInEventStream` | `local_id` (not `point_id`) lives in the EVENTS stream (ADR-0001) |
| `integration/control_test.go` | `TestControl_HappyPath` | Control command from mock BOS → `dispatch` → NATS subscriber → `ControlResult` returned |
| | `TestControl_NotWritable` | Read-only point → no NATS write, `not_writable` result |
| | `TestControl_EgressReconnect` | BOS mock restart → egress agent reconnects and resumes within 10 s |
| `integration/storeforward_test.go` | `TestSF_OutageSurvival` | Frames buffered during BOS outage are replayed on reconnect |
| | `TestSF_ImmediateSend` | Frames sent directly when BOS is reachable (no unnecessary buffering) |
| | `TestSF_DriftCounterRises` | Drift counter increments correctly when BOS falls behind |
| `integration/pointsync_test.go` | `TestPointSync_*` | Point list sync loop: initial fetch, ETag 304 no-op, diff apply, persist/reload |

Run:

```bash
go test -race ./integration/...
```

---

## Layer 2 — Live nexus-gateway stack E2E (opt-in)

Requires the gateway Docker Compose stack (`docker-compose.yml`). Tests skip
automatically when `E2E_NATS_URL` is unset (ADR-0004).

**Prerequisites:**

```bash
docker compose up -d   # or with --profile bacnet / --profile opcua
```

| Test function | Env var | What it verifies |
|--------------|---------|-----------------|
| `TestE2E_BacnetTelemetry` | `E2E_NATS_URL` | BACnet connector reads PT001..PT008 from the BBC sim → frames arrive in EVENTS stream |
| `TestE2E_OpcUATelemetry` | `E2E_NATS_URL` | OPC-UA connector reads PT001..PT008 from the OPC-UA sim → frames arrive in EVENTS stream |
| `TestE2E_BacnetControl` | `E2E_NATS_URL` | BACnet write command dispatched via NATS → connector ACKs → idempotent re-send |
| `TestE2E_OpcUAControl` | `E2E_NATS_URL` | OPC-UA write command dispatched via NATS → connector ACKs → idempotent re-send |

**CI status:** `TestE2E_OpcUATelemetry` is the automated **MVP gate** — the
`e2e-opcua` job runs on every push/PR and asserts not just EVENTS-stream arrival
but also that the gateway's `/metrics` shows `storefwd_written_total`/
`storefwd_sent_total > 0` (the frame reached mock Building OS). It scrapes the
admin API at `E2E_ADMIN_URL`, defaulting to `http://localhost:18080` when unset.
The `e2e-bacnet` job is **manual-only** (`workflow_dispatch`) because BACnet
needs host networking for Who-Is/I-Am broadcast and is flaky on hosted runners
(MVP+1, not a gate). See `.github/workflows/ci.yml`.

Run:

```bash
E2E_NATS_URL=nats://localhost:14222 \
  go test ./integration/... -run TestE2E_Bacnet -v -timeout 120s

E2E_NATS_URL=nats://localhost:14222 E2E_ADMIN_URL=http://localhost:18080 \
  go test ./integration/... -run TestE2E_OpcUA -v -timeout 240s
```

> The OPC-UA telemetry test's `storefwd_*` metrics assertion always runs;
> `E2E_ADMIN_URL` only overrides the admin API it scrapes (default
> `http://localhost:18080`). CI uses `-timeout 240s` for the OPC-UA job.

---

## Layer 3 — Building OS integration E2E (opt-in)

Requires the Building OS OSS stack (`docker-compose.oss.yaml` in
[gutp-building-os-oss](https://github.com/gutp-bim/gutp-building-os-oss)).
Tests skip automatically when the relevant `E2E_BOS_*` env var is unset (ADR-0004).

**Prerequisites:**

```bash
# In gutp-building-os-oss
docker compose -f docker-compose.oss.yaml up -d
```

| Test function | File | Env vars | Milestone | What it verifies |
|--------------|------|---------|-----------|-----------------|
| `TestE2E_BosIngress` | `bos_ingress_e2e_test.go` | `E2E_BOS_INGRESS_URL` | #43 | `GatewayIngressService`: known point accepted (Accepted=1), unknown `point_id` skipped (Accepted=0), wrong `gateway_id` rejected (Accepted=0), 3-frame batch cumulative count |
| `TestE2E_BosIngestAPI` | `bos_ingest_api_e2e_test.go` | `E2E_BOS_INGRESS_URL`, `E2E_BOS_API_URL` | #44 / M4+M5 | **M4**: gRPC `StreamTelemetry` frame accepted; **M5**: `/telemetries/hot?pointId=SOS-PT-001` reflects the ingested value within 45 s |
| `TestE2E_BosControlGate` | `bos_egress_e2e_test.go` | `E2E_BOS_API_URL` | #45 / M8 | `POST /points/{writable}/control` → 202; `POST /points/{non-writable}/control` → 403 |
| `TestE2E_BosEgressDispatch` | `bos_egress_e2e_test.go` | `E2E_BOS_EGRESS_ADDR`, `E2E_BOS_API_URL` | #45 / M7 | HTTP control → `GatewayEgressService.Connect` (gRPC, port 5052) → `egress.Agent` → NATS `cmd.bacnet.bacnet-01` → mock connector ACK |
| `TestE2E_BosReporting` | `bos_reporting_e2e_test.go` | `E2E_BOS_INGRESS_URL`, `E2E_BOS_API_URL` | #46 | All 10 SoS publishable points (SOS-PT-001..010) ingested in one stream (Accepted=10); each point readable via `/telemetries/hot` with fresh value (parallel sub-tests) |

Run:

```bash
# #43 — GatewayIngress acceptance
E2E_BOS_INGRESS_URL=localhost:5051 \
  go test ./integration/... -run TestE2E_BosIngress -v -timeout 60s

# #44 — M4/M5 ingest + API read-back
E2E_BOS_INGRESS_URL=localhost:5051 E2E_BOS_API_URL=http://localhost:5000 \
  go test ./integration/... -run TestE2E_BosIngestAPI -v -timeout 60s

# #45 — M8 control gate
E2E_BOS_API_URL=http://localhost:5000 \
  go test ./integration/... -run TestE2E_BosControlGate -v -timeout 30s

# #45 — M7 egress dispatch (requires gateway-bridge:5052 + GatewayConnectionTypes__Map__GW-SOS-001=bacnet-sim)
E2E_BOS_EGRESS_ADDR=localhost:5052 E2E_BOS_API_URL=http://localhost:5000 \
  go test ./integration/... -run TestE2E_BosEgressDispatch -v -timeout 60s

# #46 — SoS reporting (10 points)
E2E_BOS_INGRESS_URL=localhost:5051 E2E_BOS_API_URL=http://localhost:5000 \
  go test ./integration/... -run TestE2E_BosReporting -v -timeout 90s
```

### Building OS port map

| Port | Service | Role |
|------|---------|------|
| 5000 | `building-os.api` | REST API (`/telemetries/hot`, `/points/{id}/control`, …) |
| 5051 | `building-os.connector-worker` | `GatewayIngressService` (gRPC, h2c) |
| 5052 | `building-os.gateway-bridge` | `GatewayEgressService` (gRPC, h2c) |

---

## Environment variable reference

| Env var | Example | Used by |
|---------|---------|---------|
| `E2E_NATS_URL` | `nats://localhost:14222` | Layer 2 connector E2E tests |
| `E2E_BOS_INGRESS_URL` | `localhost:5051` | BOS ingress tests (#43, #44, #46) |
| `E2E_BOS_API_URL` | `http://localhost:5000` | BOS API read-back tests (#44, #45, #46) |
| `E2E_BOS_EGRESS_ADDR` | `localhost:5052` | BOS egress dispatch test (#45/M7) |
