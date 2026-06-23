# MVP Readiness

This document fixes the **MVP scope** for nexus-gateway, the **pass conditions**
that define "done", and the gaps that must close first. The strategy at this
stage is to *narrow and verify*, not to broaden: pick one real protocol, prove
telemetry reaches Building OS, and make control / outage-recovery / monitoring
verifiable.

**MVP = at least one real protocol end-to-end** (telemetry up to Building OS,
control down to a connector, store-and-forward outage recovery, observable
state). It is *not* "BACnet + OPC-UA + MQTT all complete". OPC-UA is the baseline
protocol because its E2E runs over plain TCP with no host-networking dependency.

---

## MVP Scope

### Included

| Component | In MVP |
|-----------|--------|
| Core Agent | ✅ required |
| NATS JetStream | ✅ required |
| Normalizer (`local_id → point_id`) | ✅ required |
| SQLite Store-and-Forward | ✅ required |
| gRPC Ingress (telemetry up) | ✅ required |
| gRPC Egress / control (down) | ✅ minimal required |
| Point List — file/CSV bootstrap | ✅ required |
| Point List — HTTP provisioning (#224 ETag) | β acceptable |
| Admin API (`/health` `/metrics` `/telemetry` `/connectors` `/devices`) | ✅ required |
| Admin UI (Next.js) | nice-to-have, **not** MVP-blocking |
| **OPC-UA E2E** (`integration/TestE2E_OpcUATelemetry`) | ✅ baseline scenario |
| Connector Catalog — dev/file-backed | ✅ sufficient for MVP |
| Store-and-Forward metrics on `/metrics` | ✅ required (implemented) |

### Excluded (→ MVP+1)

- **BACnet E2E** — depends on host networking for Who-Is/I-Am broadcast; keep as
  MVP+1 (works today behind the `bacnet` compose profile, just not a gate).
- **cosign production verification** — dev/file catalog is the MVP path.
- **Edge mTLS in production** — ship config example only (ADR-0007); local
  compose uses `--bos-insecure` h2c. The Building OS side is now in place
  (Traefik `passTLSClientCert` → `X-Gateway-Id`, cert-manager CN=`gateway_id`,
  enforce toggle — BOS #296/#224/#298); the gateway needs no code change.
- **Grafana dashboards / full observability stack** — `/metrics` text is enough
  for MVP.
- **upgrade-by-arbitrary-image** (`POST /connectors/{id}/upgrade?image=`) — dev
  affordance; the MVP update path is catalog-driven.

---

## Required Scenarios

The MVP is "done" when these five are reproducible (CI or documented steps):

1. **Full stack boots** — `docker compose up --build` brings every service to
   healthy.
2. **Telemetry happy path** — OPC-UA: simulator → connector → NATS JetStream →
   Normalizer → SQLite Store-and-Forward → mock Building OS.
3. **Store-and-forward outage/recovery** — with Building OS down, the buffer
   depth grows; after recovery it drains (cursor advances).
4. **Control command** — a `ControlCommand` resolves `point_id` → native address
   and reaches the owning connector, returning exactly one typed `ControlResult`.
5. **Observable state** — `/health`, `/metrics`, `/telemetry`, `/connectors`,
   `/devices` all report correct status; **store-and-forward metrics are present
   on `/metrics`** (implemented).

Plus the negative path that distinguishes misconfiguration from a bug:

6. **Point List miss** — an unknown `local_id` is dropped-and-metered
   (`normalizer_unresolved_total`), not buffered (ADR-0002/0003).
7. **Connector restart** — `POST /connectors/{id}/restart` (and auto-restart via
   the watch loop) brings a dead connector back.

---

## Required Commands

```bash
# 1. Full stack
docker compose up --build

# 2. OPC-UA baseline overlay (CI-friendly, plain TCP)
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile opcua up

# 3. Unit + race suite (also runs the in-process store-and-forward
#    outage/recovery test, integration/TestSF_OutageSurvival — scenario #3)
go test -race ./...

# 4. OPC-UA E2E smoke over the stack from step 2 (scenario #2)
E2E_NATS_URL=nats://localhost:14222 \
  go test ./integration/... -run TestE2E_OpcUATelemetry -v -timeout 120s
```

> Note: `integration/TestE2E_OpcUATelemetry` is **not** build-tagged — it skips
> automatically when `E2E_NATS_URL` is unset, so it is inert in a normal
> `go test ./...` run and only executes against the live stack (this is the CI
> `e2e-opcua` job). It asserts the OPC-UA Common Events on NATS **and** scrapes
> the gateway's `/metrics` for `storefwd_written_total`/`storefwd_sent_total > 0`,
> proving the frame reached mock Building OS. The outage/recovery scenario (#3) is
> the in-process `integration/TestSF_OutageSurvival`, which runs in the unit
> suite above.

---

## Required Metrics

`/telemetry` (JSON) already exposes `buffer_depth` and per-`point_id` `drifts`.
The Prometheus `/metrics` endpoint also exposes the store-and-forward series the
evaluation plan assumes. **Implemented:** `adminapi.handleMetrics`
(`internal/adminapi/server.go`) emits the full `storefwd_*` set alongside
`gateway_*` and `normalizer_*`.

| Metric | Source | Status |
|--------|--------|--------|
| `storefwd_buffer_depth` (gauge) | `Buffer.Depth()` | ✅ present |
| `storefwd_written_total` (counter) | `Buffer.Write` | ✅ present |
| `storefwd_sent_total` (counter) | `Forwarder` send | ✅ present |
| `storefwd_dropped_total` (counter) | `Buffer` overflow drop-oldest | ✅ present |
| `storefwd_checkpoint_total` (counter) | `Forwarder.checkpoint` | ✅ present |
| `storefwd_send_error_total` (counter) | `Forwarder` / `grpcSink` error | ✅ present |
| `normalizer_invalid_total` (counter) | Normalizer poison | ✅ present |
| `normalizer_unresolved_total` (counter) | Normalizer point-list miss | ✅ present |

The depth gauge is read off the buffer; the counters are sourced from atomic
counters on `Buffer` (`written`, `dropped`) and the `Forwarder` (`sent`,
`checkpoint`, `send_error`), surfaced through `adminapi.handleMetrics`. Covered by
`internal/adminapi/adminapi_test.go`.

---

## Known Limitations

- **BACnet host networking** — the BACnet connector needs host networking for
  Who-Is/I-Am broadcast; out of the MVP CI gate (MVP+1).
- **Edge mTLS not default in local compose** — local/dev runs `--bos-insecure`
  plaintext h2c against the gRPC services directly (no edge proxy); production
  mTLS terminates at the Building OS **Traefik** edge and is a config example for
  MVP (ADR-0007, BOS #296/#224/#298 — dependency now resolved).
- **Connector Catalog uses file-backed dev mode** — cosign production
  verification and a remote catalog are MVP+1.
- **`upgrade?image=` is a dev affordance** — the supported MVP update path is
  catalog-driven (ADR-0006).

---

## Pre-MVP Order of Work

**P0 (blocking)**

1. Expose store-and-forward metrics on `/metrics` (see Required Metrics) —
   ✅ done (**#73**; `internal/adminapi/server.go`).
2. Fix the OPC-UA E2E smoke as the single MVP baseline test —
   ✅ done (**#74**; `integration/TestE2E_OpcUATelemetry`).
3. State MVP scope (in/out) in the README — ✅ done (this doc + README pointers).
4. Unify the `docker compose` port docs (README.ja used container-internal
   `3000/8080/8090`; published ports are `13000/18080/18090`) — ✅ done.

**P1**

5. Demote `upgrade?image=` to a dev-only path; document catalog-driven update as
   the MVP update route — **#75**.
6. Keep `--bos-insecure=true` to compose/dev only; provide a TLS config example
   for the production MVP procedure — **#76**.
7. Maintain this `MVP_READINESS.md` as the gate checklist.

All open items are tracked under the **`mvp`** label; see the repository issue
list (#73–#76).
