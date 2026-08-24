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
connector** that synthesizes Common Events — no NATS connectors, no equipment:

```bash
go run ./cmd/gateway --dev-sim
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

The MQTT connector connects to any MQTT 5.0 broker. In dev you can use the
provided Mosquitto overlay; it starts a disposable broker and wires
`mqtt-connector` to it.

```bash
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml -f docker-compose.mqtt-dev.yml up --build
```

`docker-compose.mqtt.yml` defaults are aligned with `fixtures/point_list.json`:

| MQTT topic / Point List `local_id` | Point List `connector_id` | Normalized `point_id` |
|------------------------------------|---------------------------|------------------------|
| `sensors/room1/temp` | `mqtt-01` | `room1_temperature` |
| `sensors/room1/humidity` | `mqtt-01` | `room1_humidity` |

That alignment matters: connectors publish Common Events with native
`local_id`s only, and the Normalizer resolves them to `point_id`s from the Point
List. If the connector runs as `mqtt-connector` or starts with `MQTT_POINTS=[]`,
the test topics either are not subscribed or are dropped as unresolved. Keep
`CONNECTOR_ID=mqtt-01` and make `MQTT_POINTS` match the Point List unless you
also update `fixtures/point_list.json`.

Publish sample values through the dev broker:

```bash
docker run --rm --network nexus-gateway_default eclipse-mosquitto:2 \
  mosquitto_pub -h mqtt-broker -t sensors/room1/temp -m 23.7

docker run --rm --network nexus-gateway_default eclipse-mosquitto:2 \
  mosquitto_pub -h mqtt-broker -t sensors/room1/humidity -m 48.2
```

Then confirm the gateway received and normalized them:

```bash
curl -s http://localhost:18080/recent | jq
curl -s http://localhost:18080/telemetry | jq
```

Expected `/recent` entries:

```json
{
  "values": [
    { "point_id": "room1_temperature", "value": 23.7 },
    { "point_id": "room1_humidity", "value": 48.2 }
  ]
}
```

Against `mock-bos`, `/telemetry` should usually show `buffer_depth: 0` and an
empty `drifts` object after the frames are accepted.

For an external broker, omit `docker-compose.mqtt-dev.yml` and set
`MQTT_BROKER_URL` to an address reachable from inside the connector container:

```bash
MQTT_BROKER_URL=mqtt://your-broker:1883 \
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up --build
```

For AWS IoT Core, the MQTT overlay defaults to the configured ATS endpoint and
subscribes to `#`. It mounts the client certificate and
private key from the git-ignored `secrets/` directory as read-only Compose
secrets. Keep the private key out of environment variables and images. Set the
approximately 2000 exact topic entries through `MQTT_POINTS_FILE` (preferred)
or `MQTT_POINTS`; wildcard subscription reduces broker subscriptions but does
not bypass exact Point List validation. `tas/heartbeat` is acknowledged and
ignored.

Run the compose commands above from whichever checkout you actually placed
`secrets/` in — it is git-ignored, so it exists only where you put it, not in
every clone or worktree of this repo.

If your topics come from an SBCO standard point-list CSV (the same file format
`--provisioning-file` / `PROVISIONING_FILE` accepts — see the
[configuration table](../README.md#configuration-flags--env) in the README), that CSV is
**not** directly usable as `MQTT_POINTS_FILE` — the two point lists have
different schemas and different jobs. The gateway's Point List CSV resolves a
connector's `local_id` to a canonical `point_id` for normalization; the MQTT
connector's own `MQTT_POINTS_FILE` is a flat JSON array of
`{topic, device_ref, unit, writable, command_topic, payload_template}` that
tells the connector which topics to subscribe to in the first place, and it
acks-and-drops any message on a topic not listed there — wildcard subscription
does not bypass that. Generate the connector's file from the CSV with:

```bash
python3 scripts/csv-to-mqtt-points.py secrets/THX_StandardPointList_v1.confirmed.csv fixtures/mqtt/aws_iot_points.json
```

and point `MQTT_POINTS_FILE` at the result (`docker-compose.mqtt.yml` already
mounts `fixtures/mqtt/aws_iot_points.json` there by default). The gateway's
own Point List should still be pointed at the CSV directly
(`PROVISIONING_FILE=secrets/THX_StandardPointList_v1.confirmed.csv`,
`CONNECTOR_MAP=mqtt:mqtt-01`) so incoming events actually resolve to
`point_id`s instead of being dropped as unresolved (ADR-0002, ADR-0003).

Observed payloads use `datetime` as their RFC 3339 observation timestamp and a
numeric JSON `value` or numeric string. Both are converted to the gateway's
numeric telemetry value. General strings are preserved in
the first-class string value and sends it as `TelemetryFrame.value_str`. A valid
`snapshot_at` is the timestamp fallback. Payloads larger than
`MQTT_MAX_PAYLOAD_BYTES` (default 1024 bytes) are acknowledged and discarded
before JSON decoding.

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
