# Connector Wire Protocol Specification

Version: 1.0 (nexus-gateway v0.x)  
Status: Normative

This document defines the complete contract a connector container must satisfy to interoperate with nexus-gateway. It covers NATS topics, message schemas, container environment, point list format, and the Connector Catalog manifest.

Reference implementations: [`connector/bacnet/`](../connector/bacnet/), [`connector/opcua/`](../connector/opcua/), [`connector/mqtt/`](../connector/mqtt/)

---

## Contents

1. [Architecture overview](#1-architecture-overview)
2. [NATS topology](#2-nats-topology)
3. [Telemetry channel — Common Event](#3-telemetry-channel--common-event)
4. [Control channel — Write command](#4-control-channel--write-command)
5. [Container contract](#5-container-contract)
6. [Point list format](#6-point-list-format)
7. [Connector Catalog manifest](#7-connector-catalog-manifest)
8. [Idempotency requirements](#8-idempotency-requirements)
9. [Behaviour rules summary](#9-behaviour-rules-summary)

---

## 1. Architecture overview

```
Field device
    │  (BACnet / OPC-UA / MQTT / …)
    ▼
┌─────────────────────┐
│  Connector container │   publishes Common Events
│  (your code here)   │ ─────────────────────────────►  NATS JetStream
│                     │                                  stream: EVENTS
│                     │ ◄───────────────────────────── NATS core request-reply
│                     │   receives write commands        subject: cmd.<proto>.<id>
└─────────────────────┘
                                    │
                          ┌─────────▼───────────┐
                          │   nexus-gateway core  │
                          │  (normalizer / egress)│
                          └───────────────────────┘
                                    │  gRPC
                                    ▼
                             Building OS (BOS)
```

A connector has exactly two NATS interactions:

| Direction | Mechanism | Subject |
|-----------|-----------|---------|
| Connector → Gateway | JetStream publish | `evt.<protocol>.<connector_id>` |
| Gateway → Connector | Core request-reply | `cmd.<protocol>.<connector_id>` |

The gateway is responsible for everything above NATS: normalization, BOS provisioning, store-and-forward, and mTLS to BOS.

---

## 2. NATS topology

### 2.1 EVENTS stream

The gateway provisions the stream. Connectors do not create it; they only publish to it.

| Parameter | Value |
|-----------|-------|
| Stream name | `EVENTS` |
| Subject filter | `evt.>` |
| Retention | Limits (not Work Queue) |
| Storage | File |
| Max age | 48 hours |
| Max bytes | 2 GiB |
| Discard policy | Old (drop head when full) |

### 2.2 Subject naming

```
evt.<protocol>.<connector_id>
```

Examples:
- `evt.bacnet.bacnet-01`
- `evt.opcua.opcua-01`
- `evt.mqtt.mqtt-edge`

**Rules:**
- `<protocol>` must be a lowercase ASCII identifier matching the CONNECTOR_MAP key (see §5.3).
- `<connector_id>` must match the `CONNECTOR_ID` environment variable.
- Connectors **must not** publish to subjects outside `evt.<protocol>.<connector_id>`.

### 2.3 Control subject

```
cmd.<protocol>.<connector_id>
```

The gateway sends a NATS core `Request` to this subject. Connectors **subscribe** to it and must **reply** synchronously (within the request timeout, default 5 s). This is not JetStream; it is NATS core request-reply.

---

## 3. Telemetry channel — Common Event

### 3.1 Schema

All fields are required. Published as UTF-8 JSON, no envelope or framing.

```json
{
  "protocol":     "bacnet",
  "connector_id": "bacnet-01",
  "local_id":     "analogInput,0",
  "device_ref":   "device-001",
  "value":        23.5,
  "unit":         "degC",
  "quality":      "Good",
  "timestamp":    "2026-06-20T04:30:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `protocol` | string | Protocol name. Must match the subject prefix (`evt.<protocol>.*`). |
| `connector_id` | string | Value of `CONNECTOR_ID` env var. Must match the subject suffix. |
| `local_id` | string | Native protocol address of the data point. **Never** a canonical `point_id`. |
| `device_ref` | string | Opaque device identifier. Passed through to the gateway; used for reverse-lookup only. |
| `value` | number | Numeric present-value. Non-numeric values are not supported in v1. |
| `unit` | string | Engineering unit string (e.g. `"degC"`, `"Pa"`, `""`). May be empty. |
| `quality` | string | One of `"Good"`, `"Bad"`, `"Uncertain"`. |
| `timestamp` | string | RFC 3339 UTC timestamp of the observation (`…Z` suffix required). |

### 3.2 `local_id` format

`local_id` is the connector's native address for the data point, taken directly from the point list (see §6). The gateway never interprets it. Examples:

| Protocol | Example `local_id` |
|----------|--------------------|
| BACnet | `"analogInput,0"` |
| OPC-UA | `"ns=2;s=Temperature"` |
| MQTT | `"sensors/floor3/temp"` |

**MQTT:** `local_id` is the exact MQTT topic path configured in `MQTT_POINTS[].topic`. The connector subscribes to and matches topics by exact string equality — MQTT wildcard characters (`+`, `#`) are not supported. Messages arriving on a topic not present in the point list are acknowledged immediately and discarded. The gateway Normalizer looks up this topic string in the Point List to resolve the canonical `point_id`; the connector never performs this resolution.

### 3.3 `quality` semantics

| Value | Meaning |
|-------|---------|
| `"Good"` | Device read succeeded; value is reliable. |
| `"Bad"` | Device unreachable, comm failure, or hardware fault. |
| `"Uncertain"` | Device responded but with a non-fatal warning status. |

Publish `"Bad"` quality events rather than dropping them — the gateway forwards quality to BOS so it can alert operators.

### 3.4 Publish behaviour

- Use JetStream `Publish` (acknowledged publish), not core `Publish`.
- On NATS publish error, **log and skip** — do not terminate. The gateway's store-and-forward handles downstream outages; the connector must remain healthy.
- Do not buffer events in memory for retry. Each poll cycle publishes fresh values.

### 3.5 BACnet COV subscription behaviour

The BACnet connector implements COV subscriptions **in parallel with polling** (not as a replacement). The design and its pipeline implications are:

- On startup, after the initial full poll, a `_cov_loop` task is spawned for every configured point. It calls `SubscribeCOV` with `lifetime=300 s` and renews the subscription before expiry.
- Each COV notification fires `js.publish()` immediately — there is no batching or rate-limiting at the connector level.
- If `SubscribeCOV` fails for a point five consecutive times, the loop exits and that point relies on polling only. This is the correct fallback for devices or object types that do not support COV.
- COV events enter JetStream on the same subject (`evt.bacnet.<connector_id>`) and are processed by the Normalizer identically to polled events. **The `TelemetryFrame` delivered to Building OS carries no flag indicating whether the event was COV- or poll-triggered.**

The periodic poll loop continues running alongside COV subscriptions, acting as a backstop for values missed during subscription gaps.

**Latency path for a COV notification reaching Building OS:**

```
BACnet device (covIncrement threshold crossed)
  → connector _cov_loop  [< 5 ms]
  → NATS JetStream publish  [< 1 ms]
  → Normalizer Fetch (maxWait=500 ms)  [0–500 ms; typically < 50 ms under load]
  → storeforward.Buffer.Write + WriteNotify  [< 2 ms]
  → Forwarder.drain → grpcSink.Send  [< 5 ms; no send-side batching]
  → Building OS GatewayIngress StreamTelemetry
```

Total gateway-side latency is dominated by the Normalizer `maxWait` (up to 500 ms in the worst case of an otherwise-idle stream).

### 3.6 Acquisition cadence — 1-minute freshness floor

Every point must be obtainable on at least a **1-minute cycle** (the default). Change-driven
paths (BACnet COV, OPC-UA MonitoredItem subscriptions) deliver faster immediate updates *on
top of* this floor; the periodic poll is the guarantee that still holds when values are static.

- **BACnet** — periodic poll every `BACNET_POLL_INTERVAL` (default **60 s**) plus per-point COV (§3.5).
- **OPC-UA** — initial poll + a change-driven subscription (1 s publishing / 250 ms sampling)
  **and** a periodic re-poll every `OPCUA_POLL_INTERVAL` (default **60 s**) as the freshness
  floor (#110). With static server values the subscription is silent, so the re-poll is what
  keeps telemetry flowing. For an actively-changing point both paths may emit within an
  interval (a subscription event plus the next re-poll); like BACnet COV (§3.5) the frame
  carries no poll-vs-subscription flag and the Normalizer treats them identically.
- **sim** — fixed ticker. Standalone `cmd/sim-connector`: `--interval` / `SIM_POLL_INTERVAL`
  (default **60 s**). In-process `--dev-sim`: `--dev-sim-interval` flag (default **60 s**;
  lower it for fast local feedback). A non-positive interval is clamped to the 60 s default.
- **MQTT** — push-based: it emits when a broker message arrives, **and** re-publishes
  each point's last-known value once per `MQTT_FRESHNESS_INTERVAL` (default **60 s**) when
  no broker update has arrived within the interval, so a never-changing point matches the
  BACnet/OPC-UA cadence. A broker update resets the point's floor timer; a point that has
  never reported is never invented. Set `MQTT_FRESHNESS_INTERVAL=0` to disable (pure push).

### 3.7 MQTT telemetry payload formats

The MQTT connector accepts two payload formats on subscribed topics. Non-numeric or otherwise unparseable payloads are acknowledged to the broker and silently discarded (a `WARN` log is emitted).

**Format 1 — plain numeric string**

The entire payload is a UTF-8 decimal or floating-point number (whitespace is trimmed).

```
22.5
```

```
42
```

**Format 2 — JSON object with a numeric field**

A JSON object containing one of the keys `"value"`, `"Value"`, or `"v"` (checked in that order). The field value may be a JSON number or a quoted numeric string.

```json
{"value": 22.5}
```

```json
{"Value": "22.5", "unit": "Cel"}
```

```json
{"v": 42}
```

The extracted number becomes the `value` field of the emitted Common Event. Quality is always `"Good"` for successfully parsed messages; the connector has no device-level quality signal from MQTT.

---

## 4. Control channel — Write command

### 4.1 WriteRequest

Sent by the gateway as the body of a NATS core `Request`. The connector receives it on `cmd.<protocol>.<connector_id>`.

```json
{
  "control_id": "ctrl-abc123",
  "local_id":   "analogOutput,0",
  "device_ref": "device-001",
  "value":      22.0,
  "priority":   8
}
```

| Field | Type | Description |
|-------|------|-------------|
| `control_id` | string | Globally unique command identifier. Used for idempotency (§8). |
| `local_id` | string | Native address of the point to write. Matches a `local_id` in the point list. |
| `device_ref` | string | Opaque device reference from the point list. |
| `value` | number | Value to write as present-value. |
| `priority` | integer | Write priority. `0` means "use connector default". For BACnet: 1 (highest) – 16 (lowest). |

### 4.2 WriteReply

The connector **must reply** to every `Request` with a WriteReply JSON body.

```json
{ "success": true,  "response": "ok" }
{ "success": false, "response": "not_writable" }
```

| Field | Type | Description |
|-------|------|-------------|
| `success` | boolean | `true` if the write reached the device without error. |
| `response` | string | Human-readable result token. See §4.3. |

### 4.3 Standard `response` tokens

| Token | Meaning |
|-------|---------|
| `"ok"` | Write succeeded. |
| `"not_writable"` | The point exists but is not writable. |
| `"bad_request"` | Cannot parse the WriteRequest. |
| `"timeout"` | Device did not respond within the write timeout. |
| `"device_error: <detail>"` | Device returned an error. Include the error message. |
| `"in_flight"` | A duplicate `control_id` is already being processed (in-flight). |

Custom tokens are allowed but the above set must be used where semantically appropriate.

### 4.4 Timeout

Reply **before** the request context deadline. The gateway's default request timeout is 5 seconds. If the device write takes longer, reply `"timeout"` rather than missing the deadline entirely — a missed NATS reply leaves the gateway blocking until it times out on its side.

---

## 5. Container contract

### 5.1 Required environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `CONNECTOR_ID` | **Yes** | Unique connector identifier. Must match the `CONNECTOR_MAP` value for this protocol. Used as the subject suffix for both `evt.*` and `cmd.*`. |
| `NATS_URL` | **Yes** | NATS server URL. Default: `nats://localhost:4222`. Injected by the gateway at container start. |

### 5.2 Protocol-specific environment variables

The gateway passes these through from the connector registration. Protocol-specific connectors define their own variable names.

**BACnet connector:**

| Variable | Default | Description |
|----------|---------|-------------|
| `BACNET_ADDRESS` | — | BACnet device IP address or CIDR (`host/24`). |
| `BACNET_DEVICE_ID` | — | BACnet device instance number. |
| `BACNET_POINTS` | `[]` | JSON array of point configs (see §6.1). |
| `BACNET_POLL_INTERVAL` | `60` | Seconds between full polls (1-min freshness floor; §3.6). |
| `BACNET_RPM_CHUNK_SIZE` | `20` | Max points per ReadPropertyMultiple. Polling in chunks keeps each response within the device's APDU; devices without segmentation otherwise reject large reads with `segmentation-not-supported`. Lower it for devices with a small max APDU. |
| `BACNET_READ_TIMEOUT` | `5` | Per-read deadline in seconds. A slow/unresponsive device otherwise hangs the poll loop indefinitely; on timeout the chunk yields no values and polling continues. |
| `BACNET_COV_ENABLED` | `true` | Open a per-point COV subscription in addition to polling. Set `false` (poll-only) for large point counts, where thousands of COV sessions can overwhelm a device. |
| `BACNET_LOCAL_ADDRESS` | `0.0.0.0` | Local BACnet interface. |
| `BACNET_DEFAULT_WRITE_PRIORITY` | `8` | BACnet priority 1–16 used when `priority` is 0. |
| `BACNET_WRITE_TIMEOUT` | `10` | Device write timeout in seconds. |

**OPC-UA connector:**

| Variable | Default | Description |
|----------|---------|-------------|
| `OPCUA_ENDPOINT` | — | OPC-UA endpoint URL, e.g. `opc.tcp://host:4840`. |
| `OPCUA_POINTS` | `[]` | JSON array of point configs (see §6.2). |
| `OPCUA_POLL_INTERVAL` | `60` | Seconds between periodic re-polls, run alongside the change-driven subscription as the freshness floor (§3.6, #110). |
| `OPCUA_DEVICE_REF` | `opcua-server` | Device reference echoed in emitted events. |
| `OPCUA_WRITE_TIMEOUT` | `10` | Device write timeout in seconds. |

**MQTT connector:**

| Variable | Default | Description |
|----------|---------|-------------|
| `MQTT_BROKER_URL` | `mqtt://localhost:1883` | MQTT broker URL. Supported schemes: `mqtt://` (plain TCP), `mqtts://` (TLS). |
| `MQTT_CLIENT_ID` | value of `CONNECTOR_ID` | MQTT client identifier presented to the broker. Must be unique per broker. |
| `MQTT_USERNAME` | _(empty)_ | Broker authentication username. Omit if broker allows anonymous connections. |
| `MQTT_PASSWORD` | _(empty)_ | Broker authentication password. |
| `MQTT_KEEPALIVE` | `30` | MQTT KeepAlive interval in seconds. |
| `MQTT_SESSION_EXPIRY` | `0` | MQTT 5.0 session expiry interval in seconds. `0` = session ends on disconnect (clean session behaviour). |
| `MQTT_FRESHNESS_INTERVAL` | `60s` | Freshness floor (§3.6): re-publish each point's last-known value when idle this long. Go duration (`60s`, `2m`); `0` disables (pure push). |
| `MQTT_TLS_CA_FILE` | _(empty)_ | PEM CA bundle verifying the broker certificate for `mqtts://`. Empty = system roots. Ignored for plain `mqtt://`. |
| `MQTT_TLS_CERT_FILE` | _(empty)_ | Client certificate (PEM) for mutual TLS. Must be paired with `MQTT_TLS_KEY_FILE`. |
| `MQTT_TLS_KEY_FILE` | _(empty)_ | Client private key (PEM) for mutual TLS. Must be paired with `MQTT_TLS_CERT_FILE`. |
| `MQTT_TLS_INSECURE_SKIP_VERIFY` | `false` | **Dev only.** Skip broker certificate verification for `mqtts://`. Never enable in production. |
| `MQTT_POINTS` | `[]` | JSON array of point configs (see §6.3). |

### 5.3 CONNECTOR_MAP

The gateway `CONNECTOR_MAP` env var maps `protocol → connector_id`:

```
CONNECTOR_MAP=bacnet:bacnet-01,opcua:opcua-01
```

Each connector's `CONNECTOR_ID` must match the value for its protocol entry in this map. The gateway uses CONNECTOR_MAP to route provisioning updates and control commands to the right connector.

### 5.4 Network

Connectors run on the same Docker network as the gateway and NATS. The gateway injects `NATS_URL` pointing to the NATS container. Connectors must not assume a fixed NATS hostname; always read from `NATS_URL`.

Field device connectivity (BACnet UDP broadcast, OPC-UA TCP, MQTT TCP) requires appropriate Docker network access. For BACnet UDP broadcast, `host` network mode or a MACVLAN network is typically required.

### 5.5 Health check

Docker health check: the gateway inspects the container state. Connectors may expose a minimal HTTP `/health` endpoint that returns `{"status":"ok"}`, though it is not strictly required — container `running` state is the primary liveness signal.

If implemented, the health response must include the literal string `"status":"ok"` for the gateway's health monitor grep to detect it.

### 5.6 Graceful shutdown

Handle `SIGTERM`: close device connections, flush pending publishes, and exit within 10 seconds. The gateway sends `SIGTERM` before `SIGKILL`.

### 5.7 Dockerfile requirements

```dockerfile
FROM <base image>
# ...
ENTRYPOINT ["<connector-binary>"]
# No CMD required; config comes from environment variables.
```

- Do not embed credentials or device addresses in the image.
- All config must come from environment variables.
- Log to stdout/stderr (JSON or plain text). The gateway captures container logs via Docker API.

---

## 6. Point list format

The point list tells a connector which data points to poll and how to address them. It is injected via environment variables at container startup.

### 6.1 BACnet point config (per element of `BACNET_POINTS`)

```json
{
  "local_id":   "analogInput,0",
  "device_ref": "device-001",
  "unit":       "degC",
  "writable":   false
}
```

| Field | Type | Description |
|-------|------|-------------|
| `local_id` | string | BACnet object identifier: `"<objectType>,<instance>"`. |
| `device_ref` | string | Opaque device reference. Echo this in all events and write replies. |
| `unit` | string | Engineering unit. Echo this in events. |
| `writable` | boolean | Whether the gateway may send write commands for this point. |

### 6.2 OPC-UA point config (per element of `OPCUA_POINTS`)

```json
{
  "local_id":   "ns=2;s=Temperature",
  "device_ref": "opcua-device-01",
  "unit":       "degC",
  "writable":   false
}
```

`local_id` is the OPC-UA NodeId string as returned by the Browse service.

### 6.3 MQTT point config (per element of `MQTT_POINTS`)

```json
{
  "topic":            "sensors/floor3/temp",
  "device_ref":       "mqtt://floor3",
  "unit":             "Cel",
  "writable":         false,
  "command_topic":    "",
  "payload_template": ""
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `topic` | string | **Yes** | MQTT topic to subscribe to. Becomes the `local_id` in emitted events. Exact match only — wildcards (`+`, `#`) are not supported (§3.2). |
| `device_ref` | string | **Yes** | Opaque device reference echoed in all events. |
| `unit` | string | **Yes** | Engineering unit echoed in events. May be empty (`""`). |
| `writable` | boolean | No | `true` if the gateway may send write commands for this point. Default `false`. |
| `command_topic` | string | Conditional | MQTT topic the connector publishes writes to. Required when `writable` is `true`; may differ from `topic`. |
| `payload_template` | string | No | `fmt.Sprintf` template for the outbound write payload. The single argument is the `value` (float64); use `%g` for a compact decimal. Examples: `"%g"` → `"22.5"`, `{"setpoint":%g}` → `{"setpoint":22.5}`. When empty, the connector writes a plain float string. An invalid verb falls back silently to a plain float string. |

### 6.4 Connector responsibilities

- Only poll points whose `local_id` is in the point list.
- Set `writable: false` points as read-only; reply `"not_writable"` to any write command for them.
- Echo `device_ref` and `unit` unchanged in every Common Event.
- **Do not** resolve `point_id` — that is the gateway's responsibility, not the connector's.

---

## 7. Connector Catalog manifest

To be installable via the gateway Admin API (`POST /connectors/{name}/install`), a connector image must be listed in the Connector Catalog. The catalog is a JSON array of manifests.

### 7.1 Manifest schema

```json
{
  "name":                "bacnet-connector",
  "version":             "1.2.3",
  "image":               "ghcr.io/myorg/bacnet-connector",
  "digest":              "sha256:abc123...64hexchars",
  "min_gateway_version": "0.2.0",
  "permissions": {
    "network": ["bacnet-udp"],
    "mounts":  []
  },
  "signature_required": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique name in the catalog. Used as the URL path component. |
| `version` | string | Semantic version of the connector. |
| `image` | string | OCI image reference without tag or digest (registry + path only). |
| `digest` | string | `sha256:<64 hex chars>` pinned digest. Required; tag-only references are rejected. |
| `min_gateway_version` | string | Minimum nexus-gateway version required. Omit or `""` for no restriction. |
| `permissions.network` | string[] | Network capabilities declared (informational for v0.x; not enforced). |
| `permissions.mounts` | string[] | Host paths to bind-mount read-only into the container. |
| `signature_required` | boolean | If `true`, the gateway verifies a cosign signature before install (ADR-0006). |

### 7.2 Registry allowlist

By default, only images from `ghcr.io` are allowed. The gateway operator sets `CATALOG_ALLOWLIST` (comma-separated) to expand the allowlist.

### 7.3 Keyless vs. keyed signature verification

When `signature_required: true`:

- **Keyed**: set `COSIGN_KEY_FILE` on the gateway. The image signature must match the public key.
- **Keyless (Sigstore)**: set `COSIGN_IDENTITY` and `COSIGN_OIDC_ISSUER`. The certificate chain is verified against the Sigstore transparency log.

### 7.4 Catalog Server HTTP API

The gateway polls a catalog server for manifests. Any HTTP server that implements the following two endpoints is a conforming catalog server.

| Method | Path | Response | Description |
|--------|------|----------|-------------|
| `GET` | `/connectors` | `[]Manifest` | List all available manifests. |
| `GET` | `/connectors/{name}` | `Manifest` | Fetch a single manifest by `name`. Returns `404` if not found. |

Both responses are `application/json`. The gateway's auto-updater (`Updater`) calls `GET /connectors/{name}` once per installed connector per poll cycle; `GET /connectors` is called only by `GET /catalog` on the Admin API.

**Gateway configuration:**

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--catalog-url` | `CATALOG_URL` | _(unset)_ | Remote catalog server base URL. When set, overrides `--catalog-file`. |
| `--catalog-file` | `CATALOG_FILE` | _(unset)_ | Path to a local JSON `[]Manifest` file. For dev / docker-compose environments without a catalog server. |
| `--catalog-allowlist` | `CATALOG_ALLOWLIST` | `ghcr.io` | Comma-separated list of OCI registry prefixes the gateway will pull from. Images outside this list are rejected before pull. |
| `--catalog-poll-interval` | — | `10m` | How often the auto-updater polls the catalog for new connector versions. |
| `--cosign-key` | `COSIGN_KEY_FILE` | _(unset)_ | Path to cosign public key. Empty = keyless verification. |
| `--cosign-identity` | `COSIGN_IDENTITY` | _(unset)_ | Expected certificate identity for keyless cosign. |
| `--cosign-oidc-issuer` | `COSIGN_OIDC_ISSUER` | _(unset)_ | Expected OIDC issuer for keyless cosign. |

### 7.5 Gateway-side install and update operations

#### Install

Triggered by `POST /connectors/{name}/install` on the Admin API (requires `gateway-operator` role). The gateway executes the following sequence atomically per connector:

1. Fetch manifest from the catalog by `{name}`.
2. Validate: digest format, registry allowlist, `min_gateway_version`.
3. Pull the digest-pinned image reference (`image@sha256:…`). Registry is never contacted if validation fails.
4. If `signature_required: true`: verify cosign signature. Container is never created if verification fails.
5. Register the connector spec and start the container with the declared `permissions` only.

An unsigned image, a digest mismatch, or a registry outside the allowlist causes the install to fail — **the container is never started**.

#### Catalog-driven update (auto-update)

The gateway runs a background `Updater` that polls the catalog at `--catalog-poll-interval` (default **10 min**) and applies updates to all **running** connectors:

1. `GET /connectors/{id}` → compare digest; **skip if unchanged**.
2. Pull new digest-pinned image.
3. Cosign verify (if required).
4. Stop old container.
5. Start new container with updated permissions.
6. **Health soak** (default **30 s**): poll `ContainerInspect` every 50 ms. If the container exits before the soak window elapses → automatic rollback (step 7).
7. **Rollback**: start the previous image (already local — no pull needed). The failed update is logged as an error; the previous version keeps running.

Manual catalog-driven update: `POST /connectors/{id}/update` (Admin API).

#### Rollback

`POST /connectors/{id}/rollback` (Admin API) restores the connector to its previous pinned digest without a pull. The gateway retains exactly one previous image (`PrevImage`), so a maximum of one step back is supported. A second rollback call swaps current ↔ prev again.

#### Dev-only ad-hoc upgrade

`POST /connectors/{id}/upgrade?image=<ref>` swaps the image without using the catalog. Disabled by default; enable with `--allow-adhoc-upgrade` / `ALLOW_ADHOC_UPGRADE=true`. Not for production use (ADR-0006).

### 7.6 Admin API catalog endpoints (summary)

| Method | Path | Auth role | Requires | Description |
|--------|------|-----------|----------|-------------|
| `GET` | `/catalog` | viewer | `CATALOG_URL` or `CATALOG_FILE` | List all manifests from the configured catalog. |
| `POST` | `/connectors/{name}/install` | operator | same | Fetch manifest by name, verify, and install. |
| `POST` | `/connectors/{id}/update` | operator | same | Catalog-driven update for one connector (same flow as auto-update). |
| `POST` | `/connectors/{id}/rollback` | operator | — | Restore previous pinned image without a catalog call. |

---

## 8. Idempotency requirements

The gateway may re-deliver a write command if it receives no reply (network partition, connector restart). Connectors **must** deduplicate by `control_id`.

### 8.1 Required dedup behaviour

```
On receiving WriteRequest with control_id C:
  if C is cached → return cached WriteReply immediately
  if C is in-flight → return {"success": false, "response": "in_flight"}
  otherwise:
    mark C as in-flight
    execute the device write
    cache the WriteReply
    return the WriteReply
```

### 8.2 Cache bounds

Keep at least the last **1000** `control_id` → WriteReply entries. Evict the oldest on overflow (LRU or insertion order). The gateway's own dispatcher also deduplicates on its side; connector-side dedup prevents double-writes during connector restart.

---

## 9. Behaviour rules summary

| Rule | Must / Should / May |
|------|---------------------|
| Publish on `evt.<protocol>.<connector_id>` only | **Must** |
| Use JetStream acknowledged publish | **Must** |
| Reply to every `cmd.*` Request within timeout | **Must** |
| Deduplicate write commands by `control_id` | **Must** |
| Never resolve `point_id` | **Must** |
| Echo `device_ref`, `unit` unchanged in events | **Must** |
| Read all config from environment variables | **Must** |
| Log to stdout/stderr | **Must** |
| Handle SIGTERM within 10 s | **Must** |
| Publish `"Bad"` quality on device comm failure | **Should** |
| Expose `GET /health` → `{"status":"ok"}` | **Should** |
| Avoid unbounded in-memory event buffering | **Should** |
| Implement BACnet COV subscriptions (in addition to poll) | **May** |

---

## Appendix: JSON Schema (informative)

### Common Event

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["protocol","connector_id","local_id","device_ref","value","unit","quality","timestamp"],
  "properties": {
    "protocol":     { "type": "string" },
    "connector_id": { "type": "string" },
    "local_id":     { "type": "string" },
    "device_ref":   { "type": "string" },
    "value":        { "type": "number" },
    "unit":         { "type": "string" },
    "quality":      { "type": "string", "enum": ["Good","Bad","Uncertain"] },
    "timestamp":    { "type": "string", "format": "date-time" }
  }
}
```

### WriteRequest

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["control_id","local_id","device_ref","value","priority"],
  "properties": {
    "control_id": { "type": "string" },
    "local_id":   { "type": "string" },
    "device_ref": { "type": "string" },
    "value":      { "type": "number" },
    "priority":   { "type": "integer", "minimum": 0 }
  }
}
```

### WriteReply

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["success","response"],
  "properties": {
    "success":  { "type": "boolean" },
    "response": { "type": "string" }
  }
}
```
