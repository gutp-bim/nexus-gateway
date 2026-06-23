# Integration Gateway

The edge gateway that connects building equipment (BMS, IoT, field protocols) to Building OS. It collects equipment data, relays control, absorbs protocol differences, and normalizes everything into a Building OS common data model. Building OS is the System of Record; this gateway is responsible only for connection and translation.

## Language

**Building OS**:
The central System of Record that owns the digital twin (device registry, metadata, command authority). The gateway connects to it over gRPC only. Real contract lives in `../gutp-building-os-oss/proto/` (package `gatewaybridge`), which is authoritative over the design doc §8.
_Avoid_: platform, backend, server

**Digital Twin**:
Building OS's model of the building. Enriches each incoming reading from `point_id` (building/device/name/unit), so the wire frame need only carry `(gateway_id, point_id, value, timestamp)`.
_Avoid_: registry, model

**Gateway**:
A single deployed instance of this system at a building edge, identified by `gateway_id`.
_Avoid_: edge node, agent (the Core Agent is a component inside the Gateway)

**Connector**:
An isolated per-protocol container that talks to field equipment and publishes Common Events. Holds no equipment-specific or Building-OS domain model; has no dependency on other Connectors. Distributed as a **signed OCI image**, always executed **digest-pinned** (never by tag), installed/updated only through the Connector Catalog.
_Avoid_: driver, adapter, plugin

**Connector Catalog**:
The approved-manifest service (a **standalone management server**, not Building OS) that the Gateway polls to learn which Connector versions may run. Each entry carries `name`, `version`, `image`, `digest`, `min_gateway_version`, `permissions` (network/mounts), `signature_required`. The Core Agent never pulls an image the Catalog does not list, never runs an unsigned or digest-mismatched image, and never pulls from a registry outside the allowlist (ADR-0006).
_Avoid_: registry (that is the OCI Container Registry), store, marketplace

**Common Event**:
The protocol-tagged event a Connector publishes to NATS JetStream. Carries `protocol` and **native addressing only** (`local_id` + native device ref) plus the raw value/unit/quality/timestamp. Identity is not yet resolved — that is the Normalizer's job. Published on subject **`evt.<protocol>.<connector_id>`** in the single stream **`EVENTS`**; `local_id` rides in the payload, never the subject. The Normalizer is the single durable consumer on `evt.>`; replay/re-normalization filters by protocol or connector subject.
_Avoid_: message, reading, datapoint

**Normalizer**:
The single JetStream consumer that strips protocol-dependent information and unifies Point ID, Device ID, timestamp, quality, and unit, producing canonical Telemetry. The only place semantic mapping (REC/Brick/QUDT/BOT) lives.
_Avoid_: transformer, mapper, processor

**Telemetry** (wire: `TelemetryFrame`):
The canonical normalized reading produced by the Normalizer and streamed to Building OS via `gatewaybridge.GatewayIngress/StreamTelemetry` (client-streaming). Carries only `gateway_id`, `point_id`, `double value`, RFC3339 `timestamp`, and an optional `attributes` map. `value` is always numeric (bool→0/1, state/enum→numeric code); non-numeric state rides in `attributes`. No `device_id`/`building_id` on the wire — Building OS derives them from the Digital Twin by `point_id`.
_Avoid_: Common Event (that is the pre-normalized form), reading

**Control Command** (wire: `ControlCommand`):
A write pushed down from Building OS over the Egress stream: `control_id`, `point_id`, `double present_value`, `int32 priority` (BACnet write priority). The Core Agent resolves `point_id` → native object/instance and dispatches to the owning Connector over **core NATS request-reply** (never persisted — no stale replay). Always replies with exactly one definitive **Control Result** (`control_id`, `success`, `response`); failures are typed (`timeout`/`no_connector`/`not_writable`/`device_error`).
_Avoid_: command, control message

**Command Channel**:
The gateway-internal core-NATS request-reply path Core Agent → Connector for a Control Command. Non-persistent, deadline-bounded, idempotent on `control_id`. Opposite policy to the telemetry **Store-and-Forward** (which is buffered/best-effort up; commands are real-time-or-fail).

**Device**:
A piece of field equipment (e.g. an AHU) addressed by `device_id`.
_Avoid_: asset, thing, node

**Point**:
A single readable/writable signal (e.g. supply_air_temp), addressed by `point_id`. On the wire the identity is the pair **`(gateway_id, point_id)`** — the Gateway must "own" the `point_id` in Building OS's Digital Twin. `device_id` exists in the Point List for local grouping but is not sent on the wire (twin-derived).
_Avoid_: tag, variable, datapoint

**Point List**:
The shared data model (per `smartbuilding_datamodel_builder`, 1 row = 1 Point) mapping `point_id` to native addressing (`local_id`), `unit`, writeability, control schema, device grouping, and spatial context. **Source of truth is the Building OS twin** (OxiGraph `sbco:PointExt`); the Gateway holds a synced copy and converges by **diffing** against the authoritative snapshot. Drift between the two surfaces as `accepted < sent` on the Ingress Uplink.
_Avoid_: registry, config, mapping table

**local_id**:
The protocol-native address of a Point (e.g. a BACnet object instance, OPC-UA NodeId, Modbus register), as recorded in the Point List. Distinct from the canonical `point_id`.

**Quality**:
The trustworthiness of a reading (e.g. Good), unified across protocols by the Normalizer.

**Uplink**:
The gRPC link to Building OS. Two independent streams (separate scale units): **Ingress** (`GatewayIngress/StreamTelemetry`, client-streaming Telemetry up) and **Egress** (`GatewayEgress/Connect`, a single per-gateway bidirectional stream the **gateway dials out**, opens with `Hello{gateway_id}`, then receives Control Commands down and returns Control Results up). There is no DiscoveryService.
_Avoid_: upstream, sync

**Store-and-Forward**:
The SQLite-backed bounded ring buffer that retains Telemetry during an Uplink outage and forwards it in order on reconnect. Frames are sent **immediately** as they arrive; the Ingress stream is half-closed every **5 s / 1000 frames (configurable)** as an **ack checkpoint** — `StreamAck.accepted == sent` advances the cursor past the batch, `accepted < sent` advances it too (best-effort, no resend) while incrementing the per-`point_id` drift counter. The checkpoint period bounds the duplicate window on crash, not delivery latency.
_Avoid_: queue, cache, spool

## Relationships

- A **Connector** publishes **Common Events** (native addressing) to NATS JetStream.
- The **Normalizer** consumes **Common Events**, joins the **Point List** to resolve `point_id`, and produces **Telemetry**.
- **Telemetry** is buffered by **Store-and-Forward** and streamed to **Building OS** over the Ingress **Uplink**.
- **Building OS** pushes **Control Commands** down the Egress **Uplink**; the Connector writes them and a **Control Result** returns up.
- A **Device** has one or more **Points**; **Telemetry** / a **Control Command** references exactly one **Point** by `(gateway_id, point_id)`.

## Pipeline (resolved)

```
Connectors → NATS JetStream → Normalizer → SQLite Store-and-Forward → gRPC Uplink → Building OS
```

JetStream is the durable replay/back-pressure boundary placed **before** normalization, so raw Common Events can be replayed and re-normalized if mapping rules change.

## Flagged ambiguities

- (resolved) Wire identity is `(gateway_id, point_id)` only. `building_id`/`device_id` are **not sent** — Building OS derives them from the Digital Twin via the shared Point List (#181).
- (resolved) Identity mapping is owned by the **Normalizer**: Connectors emit native addressing; the Normalizer joins the Point List to assign canonical `point_id`. JetStream-before-Normalizer replay can therefore re-map after a Point List change.
- (resolved) `value` is `double` only (bool→0/1, state/enum→numeric code); non-numeric state goes in `attributes`. First-class `oneof` value is **deferred** per Building OS contract (#189). Our design must NOT introduce a `oneof`.
- (resolved) `timestamp` is an RFC3339 **string** (empty → server stamps receive time), not int64 epoch.
- (resolved) No DiscoveryService on the Uplink. Device/Point discovery is gateway-local and feeds the Point List, not a gRPC stream to Building OS.
- (resolved) The gateway→Building OS gRPC links are machine-authenticated by **mTLS terminated at the Building OS Traefik edge** (`passTLSClientCert` → `X-Gateway-Id`, cert-manager CN=`gateway_id`↔client-cert CN/SAN), h2c in-cluster; no OIDC token on the link (ADR-0007). The earlier "Envoy" note was a placeholder; the OSS stack chose Traefik. Keycloak/OIDC authenticates human operators at the Admin API only. Telemetry ingress is hosted by Building OS **ConnectorWorker** (`GatewayIngressService`), control egress by **GatewayBridge** (`GatewayEgressService`).
- (resolved) The Point List is near-static: synced once at startup then slow-polled (~10 min). The Normalizer **drops-and-meters** a Common Event whose `local_id` is unknown (treated as misconfiguration, best-effort per ADR-0002), distinct from a poison/unparseable event; neither is buffered or parked (ADR-0003).
