# Getting Started

*English / [日本語](getting-started.ja.md)*

A hands-on walkthrough: bring up the full stack, watch telemetry flow from a
simulated connector, and drive the connector lifecycle through the Admin API —
in about 10 minutes, with no physical equipment.

If you only want the project's *why* and architecture first, read the
[README](../README.md). This guide assumes you've skimmed it.

---

## 1. Prerequisites

| Tool | Version | Used for |
|------|---------|----------|
| Docker + Docker Compose | recent | the full-stack quickstart |
| Go | ≥ 1.25 | building/running the gateway directly |
| `curl` + `jq` | any | the Admin API examples below |
| Node.js | ≥ 20 | the Admin UI (only if you build it locally) |

Everything in §2–§5 needs only Docker. §6 (no-equipment dev run) needs Go.

---

## 2. Bring up the full stack

```bash
git clone https://github.com/gutp-bim/nexus-gateway
cd nexus-gateway
docker compose up --build
```

This starts five services:

| Service | Port | What it is |
|---------|------|------------|
| `admin-ui` | http://localhost:13000 | Next.js operator console (Basic-auth login by default) |
| `gateway` | http://localhost:18080 | the Core Agent + Admin API |
| `keycloak` | http://localhost:18090 | OIDC for human operators (realm `nexus-gateway`) — starts, but unused unless you opt in (§4) |
| `mock-bos` | `localhost:15051` | a stand-in for Building OS's gRPC ingress |
| `nats` | `localhost:14222` | NATS + JetStream message bus |

Wait until every service reports healthy:

```bash
docker compose ps
```

---

## 3. Verify the gateway is alive

`/health` and `/metrics` are unauthenticated, so you can hit them immediately:

```bash
# Health snapshot: uptime, goroutines, disk/mem, and per-connector liveness
curl -s http://localhost:18080/health | jq

# Prometheus-style metrics (gateway_* and normalizer_* counters)
curl -s http://localhost:18080/metrics
```

`/metrics` exposes the two best-effort drop counters from ADR-0002:
`normalizer_invalid_total` (poison events) and `normalizer_unresolved_total`
(events whose `local_id` is not in the Point List).

---

## 4. Sign in to the Admin UI (and, optionally, get an operator token)

By default this is a single local install, so there's no external identity
provider to stand up: `docker-compose.yml` leaves the gateway's
`KEYCLOAK_JWKS_URL` unset, which means the Admin API's `/connectors`,
`/devices`, etc. are unauthenticated on the Docker network — same trust
boundary as `/health`/`/metrics` above — and the Admin UI itself is the one
place a human logs in:

> Open http://localhost:13000 and sign in with the dev default
> `admin`/`admin` (`ADMIN_USERNAME`/`ADMIN_PASSWORD` in `docker-compose.yml`).
> **Change `ADMIN_PASSWORD` before anything beyond a lab** — see
> [SECURITY.md](../SECURITY.md).

Curling the Admin API directly needs no token in this mode:

```bash
curl -s http://localhost:18080/connectors | jq
```

### Optional: Keycloak SSO instead

For multi-site/SSO deployments, set `AUTH_PROVIDER=keycloak` on `admin-ui`
and uncomment the `KEYCLOAK_*` lines on both `gateway` and `admin-ui` in
`docker-compose.yml` (see the comments there), then `docker compose up
--build` again. Once running that way, the Admin API endpoints are
role-protected (operator/viewer) and tokens come from Keycloak. Grab one with
the dev `operator` user:

```bash
TOKEN=$(curl -s http://localhost:18090/realms/nexus-gateway/protocol/openid-connect/token \
  -d grant_type=password \
  -d client_id=admin-ui -d client_secret=admin-ui-secret \
  -d username=operator -d password=operator | jq -r .access_token)

echo "${TOKEN:0:20}…"   # sanity check: should print a JWT prefix
```

Dev credentials (seeded in `fixtures/keycloak/`): `operator`/`operator` (full
control) and `viewer`/`viewer` (read-only). **Change these before any non-lab
deployment** — see [SECURITY.md](../SECURITY.md).

---

## 5. Watch telemetry and drive a connector

The `-H "Authorization: Bearer $TOKEN"` header below is only meaningful if
you opted into Keycloak in §4; in the default (Basic-auth) mode `$TOKEN` is
unset and the Admin API ignores the header (it isn't checking tokens at all),
so the same commands work either way.

### See the Point List (devices & points)

```bash
curl -s http://localhost:18080/devices -H "Authorization: Bearer $TOKEN" | jq
```

Each entry maps a native `local_id` to a canonical `point_id` — the join the
Normalizer uses (ADR-0001). In the compose stack this is loaded from
`fixtures/point_list.json`.

### See telemetry health

```bash
curl -s http://localhost:18080/telemetry -H "Authorization: Bearer $TOKEN" | jq
```

`buffer_depth` is the number of **un-forwarded** frames in the Store-and-Forward
buffer — the send backlog (frames whose seq is beyond the ack cursor), not the
total row count; `drifts` is the per-`point_id` count of frames Building OS did
not accept (Point List ⇄ twin drift, ADR-0002). Against `mock-bos` both should
stay near zero.

### List and control connectors

```bash
# What connectors does the gateway know about, and are they running?
curl -s http://localhost:18080/connectors -H "Authorization: Bearer $TOKEN" | jq

# Lifecycle actions (operator role): start | stop | restart | rollback
curl -s -X POST http://localhost:18080/connectors/<id>/restart \
  -H "Authorization: Bearer $TOKEN" -i

# Recent container logs for one connector
curl -s "http://localhost:18080/logs/<id>?tail=50" -H "Authorization: Bearer $TOKEN" | jq
```

Connectors are distributed as **signed OCI images** and installed through the
Connector Catalog, never pulled by tag (ADR-0006). The compose stack uses a
file-backed catalog (`fixtures/catalog.json`); `GET /catalog` lists it.

---

## 6. Run the gateway directly (no equipment, no Docker)

For fast iteration on the Go code, run the gateway with an in-process **sim
connector** that synthesizes Common Events — no protocol connectors, no equipment.

**Prerequisite:** a JetStream-enabled NATS broker must be running — the gateway
provisions the `EVENTS` stream on startup and exits if it can't connect. Start a
standalone one, or reuse the compose stack's broker on host port 14222:

```bash
# Option A — a standalone JetStream broker on the default port (4222):
docker run --rm -p 4222:4222 nats:2.10-alpine -js
go run ./cmd/gateway --dev-sim                            # default NATS_URL=nats://localhost:4222

# Option B — reuse the compose stack's NATS (host port 14222):
NATS_URL=nats://localhost:14222 go run ./cmd/gateway --dev-sim
```

The sim publishes every 60 s by default (the 1-minute freshness floor). For fast
local feedback, lower it: `go run ./cmd/gateway --dev-sim --dev-sim-interval 5s`.

With no `--admin-jwks-url`, the Admin API runs **auth-disabled** (dev only — the
gateway logs a warning). Now `/devices`, `/telemetry`, and `/connectors` need no
token:

```bash
curl -s http://localhost:8080/telemetry | jq   # note: :8080, the gateway's default
```

This is the quickest loop for seeing the telemetry pipeline
(`sim → JetStream → Normalizer → Store-and-Forward`) end to end. See the
[configuration flags](../README.md#configuration-flags--env) for pointing it at
a real NATS, Building OS, or Connector Catalog.

---

## 7. Connecting real equipment

Two simulator siblings let you exercise the real protocol connectors without
hardware:

```bash
# OPC-UA (CI-friendly, plain TCP)
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile opcua up

# BACnet (needs host networking for Who-Is/I-Am broadcast)
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile bacnet up
```

See [`fixtures/integration/`](../fixtures/integration/README.md) and, for the
control path (Building OS → gateway → connector), the
[E2E test overview](e2e-test-overview.md).

### MQTT

The MQTT connector connects to any MQTT 5.0 broker. It requires an external broker
(e.g. [Mosquitto](https://mosquitto.org/)); no bundled simulator is provided.

```bash
MQTT_BROKER_URL=mqtt://your-broker:1883 \
MQTT_POINTS='[{"topic":"sensors/room1/temp","device_ref":"mqtt://room1","unit":"Cel"}]' \
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up
```

See [`connector/mqtt/connector.go`](../connector/mqtt/connector.go) for the full
`MQTT_POINTS` schema (fields: `topic`, `device_ref`, `unit`, `writable`,
`command_topic`, `payload_template`). Writable points also need `command_topic` set to
the broker topic the connector should publish writes to.

---

## 8. Where to go next

- **Understand the design** — the [architecture section](../README.md#architecture)
  and the seven [ADRs](adr/) record every load-bearing decision.
- **Speak the domain** — [CONTEXT.md](../CONTEXT.md) is the glossary; use its
  terms (Connector, Common Event, Telemetry, Point List, …) consistently.
- **Add a protocol connector** — the
  [extending guide](../README.md#extending-add-a-protocol-connector) and the
  reference connectors in `connector/{bacnet,opcua,mqtt}`.
- **Contribute** — [CONTRIBUTING.md](../CONTRIBUTING.md) covers the dev loop,
  test gates, and PR conventions.

---

## Troubleshooting

| Symptom | Likely cause |
|---------|--------------|
| `401 Unauthorized` in the Admin UI | Wrong `ADMIN_USERNAME`/`ADMIN_PASSWORD` (Basic-auth mode), or an expired/missing token if you opted into Keycloak — re-run §4. |
| `401 Unauthorized` on `/connectors`, `/devices`, … | Only possible in Keycloak mode (default mode leaves these open). Missing/expired token — re-run §4; Keycloak tokens are short-lived. |
| `403 Forbidden` on a `POST` action | Keycloak mode only: token is a `viewer`, not an `operator`. |
| Token request fails | Keycloak not healthy yet — `docker compose ps` and retry once it's up. |
| `/telemetry` `buffer_depth` keeps growing | The uplink to Building OS is down; frames are buffering (expected during a `mock-bos` restart). |
| Gateway can't manage connectors | The container needs the host Docker socket mounted (`/var/run/docker.sock`); see `docker-compose.yml`. |
