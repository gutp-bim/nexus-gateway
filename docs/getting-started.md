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

> **Expected in dev mode:** `docker compose logs gateway` prints three yellow
> `WARN` lines on every startup — `Building OS link is plaintext h2c
> (--bos-insecure)`, `catalog: cosign verification disabled`, and `admin: JWT
> auth disabled`. All three are intentional for this unauthenticated, TLS-less
> dev stack (ADR-0006/ADR-0007) — see [SECURITY.md](../SECURITY.md) before any
> non-lab deployment.

The `gateway` service also runs a built-in **sim connector** (`sim-01`, dev/CI
only) out of the box, so telemetry is already flowing by the time the stack
reports healthy — see §3/§5 below to watch it. No extra setup needed for the
"~10 minutes, no equipment" experience this guide promises.

---

## 3. Verify the gateway is alive

`/health`, `/health/live`, and `/metrics` are unauthenticated, so you can hit them immediately:

```bash
# Readiness: host stats + per-connector liveness + an evaluated status/components
# breakdown. status is "ok" or "degraded" (both HTTP 200); degraded names the
# unhealthy subsystem (NATS down, uplink checkpoint stale with a backlog, buffer
# near-capacity or write errors, empty Point List, a connector not running).
curl -s http://localhost:18080/health | jq

# Liveness: always {"status":"ok"} while the process is serving — this is what the
# container healthcheck targets, so a degraded-but-serving gateway is not restarted.
curl -s http://localhost:18080/health/live | jq

# Prometheus-style metrics (gateway_* and normalizer_* counters)
curl -s http://localhost:18080/metrics
```

> Degradation thresholds are tunable via `--health-checkpoint-stale` (default 60s)
> and `--health-near-capacity-frac` (default 0.9). A quiet gateway with an empty
> backlog never flaps to degraded — checkpoint staleness only accrues while frames
> are pending.

### `/metrics` series reference

| Series | Type | Meaning |
|--------|------|---------|
| `gateway_build_info{version}` | gauge | Always `1`; carries the running version as a label. |
| `gateway_uptime_seconds` / `gateway_goroutines` / `gateway_mem_alloc_mb` / `gateway_cpu_percent` | gauge | Process host stats (CPU is the GOMAXPROCS-normalized busy % over the last sample window). |
| `gateway_connectors_total` / `gateway_connectors_running` | gauge | Aggregate connector counts. |
| `gateway_connector_up{connector_id}` | gauge | Per-connector lifecycle state (`1` running, `0` stopped) — names *which* connector is down, not only "N of M". |
| `nats_connected` | gauge | `1` while the gateway holds a live NATS connection; flips to `0` on disconnect/close (also logged as structured events). |
| `uplink_connected` | gauge | `1` after a successful Building OS ack-checkpoint, `0` after a send/checkpoint failure — a stalled uplink is alertable directly. |
| `normalizer_invalid_total` | counter | Poison Common Events the Normalizer could not parse (ADR-0002 drop). |
| `normalizer_unresolved_total{reason="point_list_miss"}` | counter | Events whose `local_id` is not in the Point List (ADR-0002 drop). |
| `storefwd_buffer_depth` | gauge | Un-forwarded backlog beyond the cursor. |
| `storefwd_written_total` / `storefwd_sent_total` / `storefwd_dropped_total` | counter | Frames written / acked-as-sent / evicted at capacity. |
| `storefwd_checkpoint_total` / `storefwd_send_error_total` | counter | Successful ack-checkpoints / uplink send failures. |
| `storefwd_drift_total` | counter | Frames Building OS rejected (accepted&lt;sent, designed best-effort loss) — surfaces the drift previously visible only on `/telemetry`. |
| `storefwd_last_checkpoint_timestamp_seconds` | gauge | Unix time of the last successful ack-checkpoint. **Staleness only accrues while the backlog is non-empty**: a quiet gateway with an empty buffer reports "now", so `time() - storefwd_last_checkpoint_timestamp_seconds` is a valid staleness alert that does not fire on an idle-but-healthy link. |

> `storefwd_*` series appear only when the store-and-forward buffer is wired
> (the normal deployed path); `/metrics` still serves the `gateway_*` /
> `normalizer_*` / connectivity series without it.

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

Because the built-in `sim-01` connector (§2) is already running, every
command below returns real data immediately — no extra setup needed.

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

> **Catalog install won't work out of the box.** The bundled
> `fixtures/catalog.json` entries carry placeholder digests
> (`sha256:0000…0000`) — Install always fails until you point the catalog at
> a real, reachable OCI registry with actually-published, signed images. This
> is expected for a dev fixture, not a bug to work around locally. For a
> working connector with no registry needed, use the built-in `sim-01` (§2)
> or add your own Device/Point over MQTT (§7).

---

## 6. Run the gateway directly (no equipment, no Docker)

§2's compose stack already runs the sim connector for you — this section is
for **fast iteration on the Go code itself**: run the gateway binary directly
against a bare NATS broker, no Docker image rebuild needed. Same in-process
**sim connector** that synthesizes Common Events, no protocol connectors, no
equipment.

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

## 7. Add your own Device and its Points (MQTT walkthrough)

The integrator's core task is to onboard a **Device** and its **Points**. This
walkthrough does it end-to-end over MQTT — the one protocol you can drive by hand
with no simulator — from a **Point List** entry to visible **Telemetry**. It
assumes the stack from §2 is up.

> **Just want a working example with zero setup?** §8's [MQTT](#mqtt) subsection
> below already ships a bundled broker plus a matching Point List entry
> (`room1_temperature`/`room1_setpoint`) — no external broker, no manual edits.
> This walkthrough instead teaches the *general* pattern for onboarding a **new**
> Device/Point of your own, using a different example (`lobby_temperature`) so it
> doesn't collide with §8's pre-populated one — expect the naming to differ.

### Step 1 — describe the Point in the Point List

The **Point List** is the single source of truth that maps each protocol-native
`local_id` to a canonical `point_id` (ADR-0001); the Normalizer resolves incoming
readings against it. The compose stack loads it from
[`fixtures/point_list.json`](../fixtures/point_list.json) (`POINT_LIST_FILE`).

Add one entry for a new Point on a new Device:

```jsonc
{
  "connector_id": "mqtt-01",              // which Connector owns this Point
  "protocol": "mqtt",
  "local_id": "sensors/lobby/temp",        // protocol-native address — the MQTT topic
  "point_id": "lobby_temperature",         // canonical id used everywhere downstream
  "device_ref": "mqtt://lobby-ahu",        // groups Points into one logical Device
  "unit": "Cel",
  "writable": false                        // read-only Point (no command topic needed)
}
```

- **`point_id`** is the stable canonical identifier telemetry and control use; it
  never carries protocol addressing.
- **`local_id`** is the protocol-native address the Connector reads — for MQTT
  that is the topic it subscribes to.
- **`device_ref`** groups Points under one **Device**; entries sharing it appear as
  the same Device in `/devices`.
- **`writable`** marks whether the Point accepts control writes (a writable MQTT
  Point also needs `command_topic` — see the schema pointer below).

### Step 2 — point the MQTT Connector at it

Bring up the MQTT Connector with a matching `MQTT_POINTS` entry (its `topic` must
equal the Point List `local_id`). Point `MQTT_BROKER_URL` at any reachable MQTT
5.0 broker (e.g. a local [Mosquitto](https://mosquitto.org/)) — or, with no broker
of your own, reuse the bundled one from §8's [MQTT](#mqtt) subsection instead of
standing up external infrastructure:

```bash
# no broker of your own? bring up just the bundled one first:
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up -d mqtt-broker
```

```bash
MQTT_BROKER_URL=mqtt://your-broker:1883 \
MQTT_POINTS='[{"topic":"sensors/lobby/temp","device_ref":"mqtt://lobby-ahu","unit":"Cel"}]' \
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up -d mqtt-connector
```

### Step 3 — publish a reading

Publish a value to the topic (the connector normalizes it to a Common Event).
Substitute your broker's host — `localhost -p 11883` if you're reusing the
bundled one from Step 2:

```bash
mosquitto_pub -h your-broker -t sensors/lobby/temp -m '21.4'
```

### Step 4 — verify Telemetry arrives

The new Device and Point now show up, and the reading flows through to Telemetry:

```bash
# The new Device (device_ref) and its Point (point_id) resolve from the Point List
curl -s http://localhost:18080/devices -H "Authorization: Bearer $TOKEN" | jq

# buffer_depth / drifts stay near zero against mock-bos; a growing buffer_depth
# means the reading was accepted but the uplink is down (see Troubleshooting)
curl -s http://localhost:18080/telemetry -H "Authorization: Bearer $TOKEN" | jq
```

If `/devices` does not list your `point_id`, the gateway did not load the edited
Point List — restart it (`docker compose restart gateway`). If the Point appears
but no reading flows, the `topic` in `MQTT_POINTS` does not match the `local_id`.

---

## 8. Connecting real equipment

Two simulator siblings let you exercise the real protocol connectors without
hardware. They live in **separate repositories that must be checked out next to
this one** — cloned as siblings under the same parent directory (the compose
build contexts are `../bacnet-sim-gateway` and `../opcua-sim-gateway`):

```bash
# from the parent directory that already contains nexus-gateway/
git clone https://github.com/takashikasuya/bacnet-sim-gateway
git clone https://github.com/takashikasuya/opcua-sim-gateway
```

If the sibling directory is missing, `docker compose … --profile opcua up` fails
with a build-context error (`../opcua-sim-gateway: no such file or directory`)
rather than anything protocol-specific — clone the sibling and retry.

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

The MQTT compose path is fully **bundled** — a Mosquitto broker and a sample
publisher come up with the connector, so no external infrastructure is needed
(mirroring the BACnet/OPC-UA simulator stories):

```bash
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up --build
```

This adds a `mqtt-broker`, a `mqtt-publisher` that publishes a readable point
(`sensors/room1/temp`) every 10 s, and the `mqtt-connector`. Both MQTT points ship
in `fixtures/point_list.json`: `room1_temperature` (read-only) and `room1_setpoint`
(writable). Watch telemetry arrive:

```bash
curl -s http://localhost:18080/telemetry -H "Authorization: Bearer $TOKEN" | jq   # buffer flowing
curl -s http://localhost:18080/devices   -H "Authorization: Bearer $TOKEN" | jq   # room1 points
```

Drive the **writable** point through the Command Channel (the publisher subscribes
to the setpoint command topic and echoes writes to its own log). No live Building
OS is needed here: publish directly to the connector's `command_topic` — the same
topic the gateway publishes to on a real Command Channel write — and watch it
echo:

```bash
# in one terminal, watch for the echoed write
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml logs -f mqtt-publisher

# in another terminal, send the write (host port 11883 is the bundled broker)
mosquitto_pub -h localhost -p 11883 -t actuators/room1/setpoint/set -m '22.0'
```

**External broker instead of the bundled one:** override `MQTT_BROKER_URL` and start
just the connector:

```bash
MQTT_BROKER_URL=mqtt://your-broker:1883 \
docker compose -f docker-compose.yml -f docker-compose.mqtt.yml up mqtt-connector
```

See the `pointEnv` struct in
[`cmd/mqtt-connector/main.go`](../cmd/mqtt-connector/main.go) for the full
`MQTT_POINTS` JSON schema — it defines the wire keys (`topic`, `device_ref`,
`unit`, `writable`, `command_topic`, `payload_template`). Writable points also need
`command_topic` set to the broker topic the connector should publish writes to.

---

## 9. Where to go next

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
| `unauthorized_client` on the token request | The realm's `admin-ui` client must have **direct access grants** enabled for the password-grant command; the bundled dev realm (`fixtures/keycloak/realm.json`) already does. If you customised the realm, re-enable it. |
| `Invalid redirect_uri` on browser sign-in | The Admin UI origin (compose publishes it on port **13000**) must be registered in the realm client's `redirectUris`/`webOrigins`. The bundled dev realm registers `http://localhost:13000`; a custom realm or a changed host port needs the matching entry. |
| Token request fails | Keycloak not healthy yet — `docker compose ps` and retry once it's up. |
| `/telemetry` `buffer_depth` keeps growing | The uplink to Building OS is down; frames are buffering (expected during a `mock-bos` restart). |
| Gateway can't manage connectors | The container needs the host Docker socket mounted (`/var/run/docker.sock`); see `docker-compose.yml`. |
