# nexus-gateway

[![CI](https://github.com/gutp-bim/nexus-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/gutp-bim/nexus-gateway/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**An edge integration gateway that connects building equipment (BMS, IoT, field protocols) to [Building OS](https://github.com/gutp-bim/gutp-building-os-oss).**

*English / [日本語](README.ja.md)*

> **Glossary:** **SBCO** (Smart Building Co-creation Organization / スマートビルディング共創機構) defines the Point List schema this gateway consumes via [`smartbuilding_datamodel_builder`](https://github.com/smartbuilding-co-creation-organization/smartbuilding_datamodel_builder). **Building OS** is the cloud-side platform that acts as System of Record for provisioning and telemetry. Both are part of the **GUTP** (Green University of Tokyo Project / グリーン東大ICTプロジェクト).

It collects equipment data, relays control, absorbs protocol differences, and
normalizes everything into Building OS's common data model. Building OS is the
**System of Record**; this gateway is responsible only for **connection and
translation**.

> **Status: v0.1.0 public preview.** The MVP scope (in/out), pass conditions, and
> remaining gaps are fixed in **[MVP_READINESS.md](MVP_READINESS.md)** — OPC-UA
> telemetry/control + Store-and-Forward is the MVP baseline. BACnet, edge mTLS
> (Traefik) production deployment, cosign production verification, and BACnet COV
> control-feedback notification are planned follow-up milestones.

---

## Why this exists

> 📄 For the full positioning, system comparison (EdgeX / Azure IoT Edge / Kura /
> Hono / ThingsBoard / EMQX Neuron / OpenRemote), responsibility split with
> Building OS / SBCO, and the technical-challenge analysis, see
> **[docs/background.md](docs/background.md)** ([日本語](docs/background.ja.md)).

A building has dozens of equipment protocols — BACnet, OPC-UA, MQTT, Modbus —
each with its own addressing and semantics. Building OS wants a single,
canonical telemetry/control contract keyed by `(gateway_id, point_id)`. Something
has to sit at the edge and absorb the protocol diversity.

### Why not EdgeX Foundry?

EdgeX Foundry is an excellent **general-purpose IoT edge platform** — it targets
buildings, factories, energy, retail, and healthcare alike, and ships Device
Services, Core Metadata, Core Command, an Application Service, a Message Bus, and
a Security stack. A minimal deployment is easily **10–20 containers**.

For this project that generality is a cost, not a benefit, because EdgeX's
**Core Metadata** (Device/Profile registry, Provision Watcher) and **Core
Command** (REST → Device Service) duplicate responsibilities that **Building OS
already owns**: the Digital Twin (REC/Brick/Ditto) is the device & point
registry, and the command path is Building OS → gRPC → gateway. Adopting EdgeX
wholesale would mean operating a second registry and command service alongside
the ones Building OS already provides — which is the main reason it reads as
"heavy" here.

So nexus-gateway is deliberately closer to **Azure IoT Edge + a protocol adapter
+ a gRPC uplink** than to a full IoT platform. It still **borrows EdgeX's good
ideas** — the *Device Service* structure, *connector separation*, and the
*Common Event → pipeline* flow — without the platform weight. The connector
contract is essentially:

```
discover() → Stream[Device]
subscribe() → Stream[Telemetry]
write(cmd)  → Result
```

with the proven per-protocol OSS stacks underneath: **Eclipse Milo** (OPC-UA),
**BACpypes3** (BACnet), **Eclipse Paho** (MQTT).

---

## Architecture

```
   field equipment / simulators
        │  BACnet/IP · OPC-UA · MQTT
        ▼
  ┌─────────────┐   evt.<proto>.<id>   ┌────────────┐   TelemetryFrame   ┌──────────────────┐
  │ Connectors  │ ───────────────────▶ │ Normalizer │ ─────────────────▶ │ Store-and-Forward │
  │ (1 / proto) │   NATS JetStream     │ local_id→  │   (point_id)       │ (SQLite ring buf) │
  └─────────────┘   stream EVENTS      │  point_id  │                    └────────┬─────────┘
        ▲                              └────────────┘                              │ gRPC stream
        │ cmd.<proto>.<id>  (core NATS request-reply)                              ▼
  ┌─────────────┐        ┌──────────┐   ControlCommand   ┌────────────┐   GatewayIngress/StreamTelemetry
  │ Egress      │ ◀───── │ Dispatch │ ◀───────────────── │ Building OS │ ◀──────────────────────────────
  │ agent       │  gRPC GatewayEgress/Connect            └────────────┘   (mTLS at the Traefik edge)
  └─────────────┘
```

- **Connectors** (one isolated container per protocol) talk to equipment and
  publish **Common Events** carrying *native addressing only* — no canonical
  identity ([ADR-0001](docs/adr/0001-telemetry-pipeline-shape.md)).
- The **Normalizer** is the single durable consumer on `evt.>`. It joins the
  **Point List** to resolve `local_id → point_id` and emits **TelemetryFrames**
  (`gateway_id` + `point_id` + value + ts).
- **Store-and-Forward** is a bounded SQLite ring buffer: best-effort,
  drop-oldest, at-least-once up to Building OS
  ([ADR-0002](docs/adr/0002-best-effort-store-and-forward.md)).
- The **Ingress Uplink** streams frames to Building OS's `GatewayIngress`
  service; the **Egress agent** holds the `GatewayEgress` stream and dispatches
  inbound **Control Commands** to connectors over a deadline-bounded, idempotent
  core-NATS request-reply ([ADR-0004](docs/adr/0004-control-path-reliable-within-window.md)).

### Key decisions (ADRs)

| ADR | Decision |
|-----|----------|
| [0001](docs/adr/0001-telemetry-pipeline-shape.md) | Connectors emit native addressing; the Normalizer owns `local_id → point_id`. Wire identity is `(gateway_id, point_id)` only. |
| [0002](docs/adr/0002-best-effort-store-and-forward.md) | Store-and-Forward is best-effort (bounded ring buffer, drop-oldest, at-least-once). |
| [0003](docs/adr/0003-point-list-source-of-truth.md) | The Point List's source of truth is the Building OS twin; the gateway syncs a copy by diff. Provisioning sync > file/CSV bootstrap. |
| [0004](docs/adr/0004-control-path-reliable-within-window.md) | Control is real-time-or-fail: deadline-bounded core-NATS request-reply, idempotent on `control_id`. |
| [0005](docs/adr/0005-jetstream-topology-bounded-replay.md) | JetStream sits before the Normalizer as the durable replay/back-pressure boundary. |
| [0006](docs/adr/0006-connector-distribution-signed-oci.md) | Connectors are signed OCI images, run digest-pinned, installed via the Connector Catalog with cosign verification + rollback. |
| [0007](docs/adr/0007-transport-security-mtls-at-edge.md) | Gateway↔Building OS gRPC is mTLS terminated at the Building OS Traefik edge (`gateway_id` ↔ client-cert CN, enforced via the `X-Gateway-Id` header); h2c in-cluster. |

---

## Features

- **Protocol connectors** — BACnet (Python/BACpypes3), OPC-UA (Java/Eclipse
  Milo), MQTT (Go/Paho), plus a zero-dependency `sim` connector for smoke tests.
  Each is an isolated container holding no Building-OS domain model.
- **Telemetry + control** in one gateway: uplink streaming and a write path
  (BACnet WriteProperty, OPC-UA Write/Method, MQTT publish).
- **Point List sync** from Building OS (or a file/CSV stand-in) with diff
  convergence; near-static, so synced once then slow-polled.
- **Resilience**: bounded Store-and-Forward rides out Building OS outages; the
  Normalizer drops-and-meters poison / point-list-miss events
  (`normalizer_invalid_total`, `normalizer_unresolved_total`).
- **Security**: config-driven **TLS/mTLS** to Building OS; **Keycloak/OIDC**
  protects the Admin API & UI (operator/viewer roles).
- **Admin UI** (Next.js) — dashboard + connector lifecycle (start/stop/restart/
  upgrade), behind OIDC.
- **Lifecycle management** via the Docker Engine API; **signed-OCI** connector
  distribution through the Connector Catalog (digest-pinned, cosign-verified,
  stop→replace→health→rollback).

---

## Quickstart

> 🚀 New here? The **[Getting Started guide](docs/getting-started.md)**
> ([日本語](docs/getting-started.ja.md)) walks you from `compose up` to watching
> telemetry flow and driving a connector through the Admin API, in ~10 minutes
> with no equipment.

```bash
# 1. Full stack: NATS + mock Building OS + gateway + Keycloak + Admin UI
docker compose up --build

# 2. Verify healthy (all services should reach "healthy" within ~60 s)
docker compose ps
```

| Endpoint | URL | Notes |
|----------|-----|-------|
| Admin UI | http://localhost:13000 | Keycloak realm `nexus-gateway`; users `operator`/`operator`, `viewer`/`viewer` |
| Gateway Admin API | http://localhost:18080 | `/health`, `/metrics`, `/connectors` |
| Keycloak | http://localhost:18090 | Admin: `admin`/`admin` |
| mock Building OS (gRPC) | `localhost:15051` | `GatewayIngressService` stub for dev |
| NATS | `localhost:14222` | NATS client port; monitoring at `:18222` |

Run the gateway binary directly (no Docker):

```bash
# Prerequisite: a JetStream-enabled NATS broker must already be running — the
# gateway provisions the EVENTS stream on startup and exits if it can't connect.
# Start one (or reuse the compose-published broker on host port 14222):
docker run --rm -p 4222:4222 nats:2.10-alpine -js        # standalone JetStream broker
# …then, pointing at that broker (default nats://localhost:4222):
go run ./cmd/gateway --dev-sim                            # in-process sim connector, no equipment

# Reusing the compose stack's NATS instead (host port 14222):
NATS_URL=nats://localhost:14222 go run ./cmd/gateway --dev-sim
```

### Configuration (flags / env)

| Flag | Env | Default | Purpose |
|------|-----|---------|---------|
| `--version` | – | – | Print the gateway version and exit (flag-only) |
| `--nats` | `NATS_URL` | `nats://localhost:4222` | NATS URL |
| `--bos` | `BOS_ADDR` | `localhost:50051` | Building OS gRPC address — default for **both** the ingress and egress links; overridden per-link by the two flags below |
| `--bos-ingress-addr` | `BOS_INGRESS_ADDR` | – | Building OS **GatewayIngress** address (telemetry); overrides `--bos` for the ingress link |
| `--bos-egress-addr` | `BOS_EGRESS_ADDR` | – | Building OS **GatewayEgress** address (control plane); overrides `--bos` for the egress link. Egress terminates on a **distinct port** from ingress, so set this when the control path uses a different address or Control Commands never connect |
| `--gateway-id` | `GATEWAY_ID` | `gw-001` | Gateway identity (also the mTLS cert CN/SAN) |
| `--admin-addr` | `ADMIN_ADDR` | `:8080` | Admin API listen address |
| `--admin-jwks-url` | `KEYCLOAK_JWKS_URL` | – | Keycloak JWKS URL (empty = auth disabled) |
| `--admin-audience` | `KEYCLOAK_AUDIENCE` | `account` | Expected JWT audience |
| `--admin-issuer` | `KEYCLOAK_ISSUER` | – | Expected JWT issuer |
| `--point-list` | `POINT_LIST_FILE` | `fixtures/point_list.json` | Bootstrap fixture (used when no provisioning source) |
| `--point-list-persist` | `POINT_LIST_PERSIST` | `data/point_list.json` | Path to persist the synced Point List across restarts |
| `--provisioning-url` | `PROVISIONING_URL` | – | Building OS Point List provisioning API |
| `--provisioning-file` | `PROVISIONING_FILE` | – | File/CSV-backed Point List (dev/E2E) |
| `--provisioning-connector-id` | `PROVISIONING_CONNECTOR_ID` | `bacnet-01` | Fallback connector ID stamped on CSV-loaded entries whose protocol has no `--connector-map` entry |
| `--connector-map` | `CONNECTOR_MAP` | – | Comma-separated `protocol:connectorID` pairs, shared by the file and HTTP provisioning paths (e.g. `bacnet:bacnet-01,opcua:opcua-01,mqtt:mqtt-01`); falls back to `--provisioning-connector-id` for any protocol with no entry |
| `--point-sync-interval` | – | `10m` | Point List poll interval after initial sync |
| `--sf-db` | `SF_DB` | `data/storeforward.db` | Store-and-Forward SQLite database path |
| `--sf-cap` | `SF_CAP` | `100000` | Store-and-Forward ring buffer capacity (frames); must be positive (rejected at startup otherwise) |
| `--bos-insecure` | `BOS_INSECURE` | `false` | Plaintext h2c to Building OS — dev/CI only (ADR-0007) |
| `--bos-ca` | `BOS_CA_FILE` | – | PEM CA bundle to verify the Building OS server cert |
| `--bos-cert` | `BOS_CERT_FILE` | – | Client certificate for mTLS to Building OS |
| `--bos-key` | `BOS_KEY_FILE` | – | Client private key for mTLS to Building OS |
| `--bos-servername` | `BOS_SERVER_NAME` | – | Override server name in Building OS cert verification |
| `--dev-sim` | `DEV_SIM` | `false` | Run the in-process sim connector (non-production, ADR-0001) |
| `--dev-sim-interval` | – | `60s` | Publish interval for `--dev-sim`; lower it (e.g. `5s`) for fast local feedback |
| `--catalog-file` | `CATALOG_FILE` | – | File-backed Connector Catalog (JSON `[]Manifest`); enables `POST /connectors/{name}/install` |
| `--catalog-url` | `CATALOG_URL` | – | Remote Connector Catalog base URL (overrides `--catalog-file`) |
| `--catalog-poll-interval` | – | `10m` | How often the Updater polls the catalog for new connector versions (ADR-0006) |
| `--allow-adhoc-upgrade` | `ALLOW_ADHOC_UPGRADE` | `false` | Enable dev-only `POST /connectors/{id}/upgrade?image=`; MVP update path is catalog-driven (ADR-0006) |
| `--catalog-allowlist` | `CATALOG_ALLOWLIST` | `ghcr.io` | Comma-separated list of allowed OCI registries (ADR-0006) |
| `--cosign-key` | `COSIGN_KEY_FILE` | – | Path to the cosign public key for connector-image signature verification (ADR-0006); empty = keyless |
| `--cosign-identity` | `COSIGN_IDENTITY` | – | Expected certificate identity for **keyless** cosign verification (ADR-0006) |
| `--cosign-oidc-issuer` | `COSIGN_OIDC_ISSUER` | – | Expected OIDC issuer for keyless cosign verification (ADR-0006) |

> **Production (ADR-0006):** set the cosign flags (`--cosign-key`, or `--cosign-identity` + `--cosign-oidc-issuer` for keyless) so connector images are signature-verified before install/update. With none set, verification is disabled and the gateway logs a warning at startup — acceptable for local/dev only.

> `SF_CAP` and `--version` are new in the observability quick-wins change; the `--sf-cap` env fallback mirrors `SF_DB`.

### Simulator integration (no equipment)

The sibling repos `../bacnet-sim-gateway` and `../opcua-sim-gateway` provide
standard-compliant BACnet/IP and OPC-UA simulators. See
[`fixtures/integration/`](fixtures/integration/README.md):

```bash
# OPC-UA E2E (CI-friendly, plain TCP)
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile opcua up

# BACnet E2E (requires host networking for Who-Is/I-Am broadcast)
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile bacnet up
```

### Running with a live Building OS

Point the gateway at the real Building OS stack instead of mock-bos:

```bash
# Building OS OSS stack (see github.com/gutp-bim/gutp-building-os-oss)
docker compose -f /path/to/gutp-building-os-oss/docker-compose.oss.yaml up -d

# Start gateway with the SPLIT BOS ingress + egress addresses and the SoS Point List.
# Telemetry ingress and the control-plane egress terminate on DISTINCT ports
# (egress on 5052, not 5051) — set both, or Control Commands silently never connect.
GATEWAY_ID=GW-SOS-001 \
BOS_INGRESS_ADDR=localhost:5051 \
BOS_EGRESS_ADDR=localhost:5052 \
BOS_INSECURE=true \
PROVISIONING_FILE=/path/to/mvp-pointlist.csv \
go run ./cmd/gateway
```

> `BOS_INSECURE=true` (plaintext h2c) is **dev/CI only** — it is for local runs
> with no edge proxy. Do not use it in production.

#### Production: TLS/mTLS to Building OS (ADR-0007)

In production the gateway↔Building OS gRPC link is **mTLS terminated at the
Building OS Traefik edge** (`TLSOption` + `passTLSClientCert`), with the
gateway's `gateway_id` bound to the client certificate's CN (cert-manager-issued).
The edge injects a trusted `X-Gateway-Id` header from the cert, which Building OS
enforces equals the frame's `gateway_id`. Drop `--bos-insecure` and provide the
CA + client cert/key instead:

```bash
GATEWAY_ID=GW-SOS-001 \
BOS_ADDR=bos.example.com:443 \
BOS_CA_FILE=/etc/nexus/tls/ca.pem \
BOS_CERT_FILE=/etc/nexus/tls/gateway.crt \   # CN/SAN encodes GATEWAY_ID
BOS_KEY_FILE=/etc/nexus/tls/gateway.key \
BOS_SERVER_NAME=bos.example.com \            # optional: override SNI/verify name
PROVISIONING_URL=https://bos.example.com/provisioning \
go run ./cmd/gateway
```

- Omit `--bos-cert`/`--bos-key` for **server-only TLS** (CA verification without
  a client cert); include them for **mTLS**.
- The cert CN ↔ `gateway_id` binding is what Building OS's ownership check
  assumes. The gateway sends **no** `X-Gateway-Id` header itself — the Traefik
  edge supplies it from the cert. See [SECURITY.md](SECURITY.md) and
  [ADR-0007](docs/adr/0007-transport-security-mtls-at-edge.md).

For the full E2E test suite against Building OS, see
**[`docs/e2e-test-overview.md`](docs/e2e-test-overview.md)**.

#### Keycloak: local dev only — use Building OS IdP in production

The Keycloak service in `docker-compose.yml` is **local dev / E2E / demo only**
(`admin`/`admin` credentials, `start-dev` mode). Two distinct auth concerns exist:

| Concern | Mechanism |
|---------|-----------|
| Human operators (Admin UI / Admin API) | Keycloak / OIDC — Bearer JWT, `realm_access.roles` |
| Gateway ↔ Building OS machine auth | **mTLS** — Keycloak is not involved |

In production, point both the gateway and Admin UI at the **Building OS Keycloak**
(or your organisation's shared IdP) and omit the bundled `keycloak` container.
The Building OS Keycloak realm needs at least two realm roles: `gateway-operator`
and `gateway-viewer`. Example production env vars:

```env
# Gateway
KEYCLOAK_JWKS_URL=https://auth.example.com/realms/building-os/protocol/openid-connect/certs
KEYCLOAK_ISSUER=https://auth.example.com/realms/building-os
KEYCLOAK_AUDIENCE=nexus-gateway-admin-api   # prefer a dedicated audience over "account"

# Admin UI
KEYCLOAK_ID=nexus-gateway-admin-ui
KEYCLOAK_SECRET=<production-secret>
KEYCLOAK_ISSUER=https://auth.example.com/realms/building-os
NEXTAUTH_URL=https://gateway-admin.example.com
NEXTAUTH_SECRET=<random-secret>
ADMIN_API_URL=https://gateway-admin-api.example.com
```

Use [`docker-compose.external-keycloak.yml`](docker-compose.external-keycloak.yml)
as a ready-made compose override for integration / production environments.

| Environment | Keycloak |
|-------------|----------|
| Local dev / CI / E2E | Bundled (this repo) |
| Building OS integration | Building OS Keycloak |
| Production | Building OS Keycloak or org-wide IdP |
| Gateway ↔ Building OS | mTLS — no Keycloak |

---

## Extending: add a protocol connector

A connector is an independent container that:

1. reads only the **native addresses** it must poll/subscribe from the Point List;
2. publishes **Common Events** to JetStream subject `evt.<protocol>.<connector_id>`
   carrying `protocol` + native `local_id` + value/unit/quality/timestamp — **no
   `point_id`** (the Normalizer assigns it);
3. subscribes to `cmd.<protocol>.<connector_id>` for **Control Commands** and
   replies with a typed result, idempotent on `control_id`.

Use the per-language reference connectors (`connector/{bacnet,opcua,mqtt}`) as
templates. Package it as a signed OCI image and list it in the Connector Catalog
to have the Core Agent run it digest-pinned (ADR-0006).

For the full contract — NATS topics, Common Event JSON schema, write command
request/reply, container env vars, Point List format, Connector Catalog manifest,
and idempotency rules — see
**[`docs/connector-spec.md`](docs/connector-spec.md)**.

## Extending: add a Northbound destination

Normalised `TelemetryFrame`s reach the Northbound transport through the
`uplink.FrameSink` interface (`Send` + `Checkpoint`).  The Building OS gRPC
stream (`grpcSink`) is the reference adapter; to send frames to an additional
destination — an MQTT broker, a REST endpoint, or a secondary BOS — implement
`FrameSink` and inject it into `uplink.NewForwarder`.

For the full contract, the `grpcSink` walkthrough, the fan-out pattern, and
wiring instructions — see
**[`docs/forwarder-extension.md`](docs/forwarder-extension.md)**.

---

## Development

```bash
go build ./...
go test -race ./...           # Go pipeline + connectors
cd admin-ui && npm run type-check && npm run build
```

CI (`.github/workflows/ci.yml`) runs the Go build/test and the Admin UI
type-check/build on every PR.

### Module seams (testability)

The behaviors the ADRs describe are concentrated into **deep modules** — a small
interface that is also the unit-test surface, so each is exercised in-process
without a live NATS/gRPC/Docker stack ([EP-011](docs/backlog/epic/EP-011-architecture-deepening.md)):

| Module | Seam | What it owns |
|--------|------|--------------|
| `uplink.Forwarder` | `FrameSink` (`Send` + `Checkpoint`) | ADR-0002 checkpoint/advance/drift/replay policy; gRPC client-streaming is the `grpcSink` adapter. |
| `lifecycle.HealthMonitor` | `GatewayMetrics` + `ConnectorProber` | host stats (uptime/mem/disk) vs. container liveness, each probed and tested independently. |
| `pointlist.Resolver` / `ReverseResolver` | forward + reverse lookup | the single Point List, consumed by the Normalizer (forward) and control Dispatch (reverse). |
| `adminapi` | `NewServer` / `NewSecureServer` + `ServerOptions` | one no-auth and one JWT constructor over a shared builder; optional sources via a single struct. |

E2E tests in `integration/` require a live stack and skip automatically
without the relevant `E2E_*` env vars (ADR-0004). See
**[`docs/e2e-test-overview.md`](docs/e2e-test-overview.md)** for the full
test landscape — in-process CI tests, live connector stack tests, and
Building OS integration tests.

---

## Contributing & security

- **Contributing** — dev setup, test gates, and PR conventions are in
  [`CONTRIBUTING.md`](CONTRIBUTING.md). Start from the
  [Getting Started guide](docs/getting-started.md).
- **Security** — report vulnerabilities privately via
  [`SECURITY.md`](SECURITY.md) (GitHub private advisories); please don't open a
  public issue for them.

---

## Academic context

This repository is also used as the implementation artifact for an academic evaluation of edge-gateway architecture for smart buildings. The evaluation plan (`docs/evaluation-plan.ja.md`) and performance test suite (`test/e2e/eval_*.go`) are included in the public repository to enable reproducibility.

## License

Apache-2.0. See [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE) for dependency attributions.
