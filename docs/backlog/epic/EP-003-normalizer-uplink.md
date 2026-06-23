# EP-003: Normalizer, Ingress Uplink & Store-and-Forward

**Status:** Prod
**Priority:** P0

## Goal

This epic delivers the telemetry path from raw Common Events to Building OS: the Normalizer resolves identity and unifies units/quality/timestamps, the Store-and-Forward ring buffer absorbs uplink outages, and the Ingress uplink streams `TelemetryFrame`s under the Building OS contract. This is the gateway's core value proposition.

The contract is **owned by Building OS** (`../gutp-building-os-oss/proto/`, package `gatewaybridge`): `GatewayIngress/StreamTelemetry`, client-streaming, ack on stream close. There is **no DiscoveryService** and the gateway authors no public proto. Delivery is **best-effort** (ADR-0002), not no-loss.

## Acceptance Criteria

- [ ] Normalizer is the single durable pull consumer on `evt.>` of the `EVENTS` stream (ADR-0005); it joins the Point List to resolve `local_id` → canonical `point_id`, unifies unit/quality/timestamp, and produces `TelemetryFrame{gateway_id, point_id, double value, RFC3339 timestamp, attributes}`.
- [ ] `value` is double only: bool→0/1, state/enum→numeric code; non-numeric state rides in `attributes`. No `oneof` is introduced (deferred per Building OS #189).
- [ ] Semantic mapping (REC/Brick/QUDT/BOT) is hosted in the Normalizer (interface stubbed for MVP), never in connectors.
- [ ] `gatewaybridge` protos are vendored from `gutp-building-os-oss` and managed with Buf, including breaking-change detection against the vendored baseline.
- [ ] Telemetry reaches Building OS over gRPC and only over gRPC.
- [ ] Store-and-Forward is a **bounded SQLite ring buffer** (drop-oldest on overflow, ADR-0002) that retains Telemetry during an uplink outage and forwards in order on reconnect.
- [ ] Uplink sends frames **immediately** on arrival and half-closes the stream every **5 s / 1000 frames (configurable)** as an ack checkpoint; `StreamAck.accepted == sent` advances the cursor; `accepted < sent` advances the cursor too (no resend) and increments a per-`point_id` drift counter.
- [ ] EVENTS stream is provisioned limits-based: maxAge 48 h + maxBytes 2 GB (configurable), DiscardOld (ADR-0005).
- [ ] Re-normalization replay works at protocol/connector subject granularity within the retention window.

## Child Features

- [ ] FEAT-011: Vendored `gatewaybridge` contract + Buf setup (codegen, breaking-change detection)
- [ ] FEAT-012: Normalizer (identity resolution via Point List, unit/quality/timestamp unification)
- [ ] FEAT-013: Ingress uplink client (immediate send + periodic ack checkpoint, reconnect/backoff, drift counter)
- [ ] FEAT-014: SQLite Store-and-Forward ring buffer (bounded, cursor per ack checkpoint)
- [ ] FEAT-015: Semantic mapping interface (stub)

## Dependencies

- EP-006 Point List Sync — the Normalizer's `local_id`→`point_id` resolution uses the shared resolver.
- `EVENTS` stream provisioning is shared infrastructure with EP-002 (publisher side).
