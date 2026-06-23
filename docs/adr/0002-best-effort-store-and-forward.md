# Best-effort telemetry delivery with a bounded store-and-forward ring buffer

**Status:** accepted — 2026-06-13

Telemetry delivery to Building OS is **best-effort**: slight loss and reordering are acceptable (product-owner decision). We target at-least-once with bounded duplicates on reconnect — **not** exactly-once, and not strict ordering. Store-and-Forward is a **bounded SQLite ring buffer**: when full, it drops the oldest readings.

This is forced by the real Ingress contract (`gatewaybridge.GatewayIngress/StreamTelemetry`): `StreamAck` returns only a cumulative `accepted` **count**, the `TelemetryFrame` has no per-frame id/sequence, and the Building OS `telemetry` hypertable has **no unique constraint** on `(point_id, time)` — so the server does not reliably de-duplicate. Chasing exactly-once would require a Building OS contract change.

## How it works

- Forward in bounded batches; advance the buffer cursor per batch on the returned `StreamAck.accepted`.
- On stream/connection failure before the ack, replay the whole un-acked batch (duplicates tolerated; keep batches small to bound the window).
- `timestamp` is **always** set to the source observation time (never empty). This keeps readings correct under replay and lets a future Building OS `(point_id, time)` unique index de-dup retroactively.
- `accepted < sent` means the gateway sent points the twin does not recognise/own (Point List ⇄ twin drift). These never succeed on retry, so we do **not** dead-letter or retry them; we advance past them and expose a **per-`point_id` drift counter** for observability. See [0003](./0003-point-list-source-of-truth.md).
