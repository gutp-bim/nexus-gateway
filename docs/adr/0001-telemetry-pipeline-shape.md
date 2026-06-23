# Telemetry pipeline: JetStream before the Normalizer, Normalizer owns identity

**Status:** accepted — 2026-06-13

The telemetry path is **Connectors → NATS JetStream → Normalizer → Store-and-Forward → gRPC Ingress Uplink**. Connectors publish raw, protocol-tagged Common Events carrying *native* addressing only (`local_id`, native device ref); they hold no canonical identity and no domain model. The Normalizer is the single JetStream consumer that joins the shared Point List to resolve the canonical `point_id`, unifies unit/quality/timestamp, and produces the `TelemetryFrame`.

We place JetStream **before** normalization (not after) so that raw Common Events are durable and **replayable**: when the Point List changes (a `local_id`→`point_id` remap, a unit change), we can re-run the Normalizer over retained raw events and get corrected Telemetry. If Connectors baked canonical `point_id` into the event, that identity would be frozen in the stream and replay could not fix the exact change it exists to absorb.

## Consequences

- The Normalizer is the *only* place identity/semantic mapping lives, matching "mapping never in Connectors." Point List churn reloads one component, not every Connector.
- Connectors still read the Point List — but only for the native addresses to poll/subscribe; they ignore the canonical columns. Minor redundant distribution, accepted for decoupling.
- Wire identity is `(gateway_id, point_id)` only; `device_id`/`building_id` are twin-derived and not sent.

## COV events share the same pipeline

BACnet Change-of-Value notifications enter the pipeline at exactly the same point as polled values: the connector publishes a Common Event to JetStream immediately on each COV notification, with no additional buffering. From the Normalizer's perspective a COV-triggered event is indistinguishable from a poll-triggered one.

End-to-end latency budget for a single COV notification:

| Stage | Latency |
|-------|---------|
| connector `_cov_loop` → `js.publish()` | < 5 ms (async, no batching) |
| NATS JetStream queuing | < 1 ms |
| Normalizer pull consumer (`Fetch maxWait=500 ms`) | 0–500 ms (returns immediately when a message is waiting) |
| `storeforward.Buffer.Write()` + `WriteNotify` signal | < 2 ms (SQLite WAL) |
| `Forwarder.drain()` → `grpcSink.Send()` | < 5 ms (triggered by `WriteNotify`, no batch wait) |
| gRPC client-streaming send to Building OS | network RTT |

The 500 ms Normalizer `maxWait` is the dominant variable. Under sustained load the fetch returns well before the timeout; it only approaches 500 ms when the stream is idle and a single COV fires into silence.

The `TelemetryFrame` does not carry a trigger-source flag (`COV` vs `POLL`). Applications that must distinguish them would need a separate subject namespace or a frame extension — neither is implemented today.
