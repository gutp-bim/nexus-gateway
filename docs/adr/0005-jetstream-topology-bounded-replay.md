# JetStream topology: one EVENTS stream, connector-grained subjects, bounded drop-oldest retention

**Status:** accepted — 2026-06-13

Common Events flow through a **single JetStream stream `EVENTS`** on subjects **`evt.<protocol>.<connector_id>`**. The Normalizer is the single durable pull consumer on `evt.>`. `local_id` rides in the payload, never the subject — point-grained subjects would explode cardinality and force escaping of protocol-native characters NATS subjects forbid, and the identity is not canonical yet anyway (ADR-0001). The Command Channel mirrors the shape on core NATS (no stream): `cmd.<protocol>.<connector_id>`.

Retention is **limits-based with `DiscardOld`**: maxAge **48h** and maxBytes **2GB**, both configurable per site. Connectors publish async with a bounded in-flight ack window; they never block on a full stream — the broker drops the oldest events instead.

We chose limits/DiscardOld over interest-based retention or publisher back-pressure because the pipeline's delivery contract is already **best-effort with drop-oldest** (ADR-0002). Making the broker stricter than the store-and-forward buffer downstream would buy nothing: events saved by a blocking publisher would still be droppable later. Interest-based retention (delete on Normalizer ack) is ruled out directly by ADR-0001 — it would destroy the raw events whose retention exists to allow re-normalization replay.

## Consequences

- **The re-normalization replay window equals the retention window** (48h / 2GB). A Point List fix can only re-map events still retained; older readings stay as originally normalized.
- Replay is filtered at protocol or connector granularity via subject filters, never per point.
- One stream means one provisioning step and uniform limits; per-protocol tuning is not possible without splitting the stream (accepted trade-off).
- Connector SDK must implement async publish with a bounded ack window; a connector that outruns the broker loses oldest data, not newest.
