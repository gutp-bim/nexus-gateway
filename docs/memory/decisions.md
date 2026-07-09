---
name: decisions
description: Key design decisions and their rationale for nexus-gateway
metadata:
  type: project
---

# Design Decisions

> Cross-reference with docs/adr/ for formally recorded decisions.
> This file captures informal or in-progress reasoning.

## Settled Decisions (from design requirements)

- **Building OS is the System of Record.** Gateway responsibility is limited to connection + translation. No device registry, metadata registry, or command authority here. → candidate ADR-001.
- **Azure IoT Edge-like structure, not EdgeX Foundry / Eclipse Hono / Kubernetes.** Chosen to avoid duplicating Building OS responsibilities and to stay lightweight for building-edge use. → candidate ADR-002.
- **gRPC is the only Building OS contract.** Internal implementation may change; the gRPC API must not break. Managed via Protobuf + Buf with breaking-change detection. → candidate ADR-003.
- **Per-protocol connector containers, fully isolated.** No inter-connector dependencies; connectors emit common event format only. → candidate ADR-004.
- **Semantic mapping (REC/Brick/QUDT/BOT) belongs in the Normalizer**, never in connectors. → candidate ADR-005.
- **Go for the Core Agent** — Docker SDK affinity, concurrency, single binary, cross-platform.
- **NATS JetStream** for internal connector event transport (pub/sub, replay, short-term retention).
- **SQLite** for local store-and-forward buffering during uplink outages.
- **Keycloak (OIDC/OAuth2)** for Admin UI, Admin API, and Building OS connection auth.

## Settled in design review (grill-with-docs session, 2026-06-13)

- **Telemetry pipeline = Connectors → NATS JetStream → Normalizer → SQLite Store-and-Forward → gRPC Uplink.** JetStream sits *before* normalization so raw Common Events can be replayed and re-normalized after a Point List change. → candidate ADR.
- **Building OS contract is authoritative over design doc §8.** Real proto lives in `../gutp-building-os-oss/proto/` (package `gatewaybridge`). Ingress = `GatewayIngress/StreamTelemetry` (client-streaming `TelemetryFrame{gateway_id, point_id, double value, RFC3339 timestamp, attributes}`). Egress = `GatewayEgress/Connect` (gateway-dialed bidi; `ControlCommand{control_id, point_id, present_value, priority}` / `ControlResult`). No DiscoveryService.
- **Wire identity is `(gateway_id, point_id)` only.** `building_id`/`device_id` are not sent; Building OS derives them from the twin (#181).
- **Normalizer owns native→`point_id` resolution** via the Point List. Connectors emit native addressing only.
- **`value` is `double` only** (bool→0/1, state/enum→numeric code; non-numeric → `attributes`). No `oneof` (deferred on Building OS side, #189). `timestamp` is RFC3339, always set to source observation time.
- **Delivery is best-effort** (slight loss / reordering acceptable, per product owner). At-least-once with bounded duplicates on reconnect; no exactly-once. Store-and-Forward = bounded SQLite **ring buffer** (drop-oldest on overflow). Cursor advances per batch on `StreamAck.accepted`; `accepted < sent` is surfaced as a **per-`point_id` drift counter** (no dead-letter table). → candidate ADR.
- **Point List source of truth = Building OS twin** (OxiGraph `sbco:PointExt`). Gateway pulls + diffs to converge. Requires a new gateway-scoped, versioned point-list provisioning API on Building OS → `gutp-building-os-oss` issue #224.
- **Control path is reliable-within-window, never replayed** (ADR-0004). Core Agent holds the Egress bidi stream; Core→Connector via core NATS request-reply (no persistence); always returns one definitive (typed) `ControlResult`; Core crash mid-command → Building OS timeout → operator retries with new `control_id`. Same Point List resolver serves both directions (native↔point_id).

## Settled in grill session (2026-06-13, issue-prep)

- **NATS topology: single stream `EVENTS`, subjects `evt.<protocol>.<connector_id>`.** Normalizer = single durable pull consumer on `evt.>`. `local_id` stays in the payload (subject cardinality low; replay filters at protocol/connector granularity). Command Channel subjects are core-NATS `cmd.<protocol>.<connector_id>` (no stream). → ADR-0005.
- **EVENTS retention: limits-based, maxAge 48h + maxBytes 2GB (configurable), DiscardOld.** Publishers never block (async publish, bounded in-flight ack window in the connector SDK). Consequence: the re-normalization replay window equals the retention window. Consistent with ADR-0002 best-effort. → ADR-0005.
- **Ingress send = immediate, ack = periodic checkpoint.** `StreamAck` only arrives on stream close (client-streaming), so the uplink sends frames the moment they arrive and half-closes the stream every **5 s / 1000 frames (configurable)** to collect the ack and advance the S&F cursor. `accepted < sent` → drift counter, no resend (no per-frame identification in the cumulative ack; resend would loop on permanently-rejected frames). Checkpoint period = max duplicate window on crash, NOT delivery latency (user concern: minimal lag to Building OS — satisfied).

- **The MQTT connector is Go; the Modbus connector is planned (Go), not yet implemented.** paho.golang (native MQTT 5.0); toolchain/SDK assets shared with the Core Agent; minimal images. When Modbus lands it will follow the same Go pattern. BACnet stays Python only because of BACpypes3, OPC-UA stays Java only because of Eclipse Milo — stack-driven, not policy.
- **Connector distribution = catalog-mediated signed OCI images (ADR-0006, EP-007).** Digest-pinned execution only; cosign verification mandatory (unsigned / digest-mismatch / non-allowlisted registry ⇒ refuse); SBOM+scan gate in CI. **Catalog host = standalone management server** (user decision — not Building OS). Update detection = poll-only (dial-out principle). Replacement = stop→replace→health→rollback, NOT canary (connectors hold exclusive equipment connections; side-by-side would double COV subscriptions/events; brief gap acceptable under ADR-0002). Scope change: connector OTA now in vision scope, firmware OTA stays out. GHCR for MVP, Harbor for air-gapped.
- **Backlog: ADR-0004 control path → new EP-005; ADR-0003 Point List sync → new EP-006 (both P0).** Control is a vertical feature spanning Core Agent/Connector/Uplink; Point List sync carries a cross-repo dependency (gutp-building-os-oss #224) whose delivery risk is tracked independently. Folding either into EP-001/002/003 would bloat those epics past one-ADR resolution.

## Pending Decisions
- ~~SQLite buffer ordering/retention/replay semantics~~ → settled (immediate send + periodic ack checkpoint, above; ring buffer per ADR-0002).
- ~~Normalizer ID-mapping configuration format and source of truth~~ → settled, ADR-0003 (Building OS twin, diff sync).
