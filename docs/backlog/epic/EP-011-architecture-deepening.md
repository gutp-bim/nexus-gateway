# EP-011: Architecture Deepening & Building OS #224 Alignment

**Status:** In progress
**Priority:** P1 (FEAT-031's P0 gap is now closed — see Progress)

## Progress

A 2026-06-15 architecture review (the `/improve-codebase-architecture` pass)
walked the codebase against this epic and found two of its premises already
overtaken by landed work, plus four further shallow seams worth deepening. State
as of that review:

- **FEAT-030 — pure Normalizer decision: done.** `Normalize` is already a pure
  `(Common Event, Resolver) → Outcome{frame|poison|miss}` function with a public
  `Outcome` type. The only residual friction is that the JetStream consumer
  (stream/consumer/subject) is still created inside the Normalizer constructor —
  a follow-up adapter extraction, not a re-write.
- **FEAT-031 — P0 API mismatch: closed.** `provisioning.HTTPClient` already
  speaks the real #224 contract (`GET /gateways/{id}/pointlist`,
  `If-None-Match`/304, `?since=` full/delta). What remains is the *structural*
  deepening (one owning module for the file-bootstrap → provisioning-override →
  blocking-first-load → forward+reverse lifecycle), which is no longer a P0.
- **FEAT-029 — S&F policy seam: done.** First as the pure `storeforward.ApplyAck`
  function, then completed by extracting `uplink.Forwarder` behind the
  `FrameSink` seam (`Send` + `Checkpoint`); gRPC client-streaming is now the
  `grpcSink` adapter. The checkpoint/advance/drift/replay rules are tested
  in-process with no gRPC stack.

### Additional deepenings landed (beyond the original FEAT list)

These came out of the same review — shallow seams not captured by FEAT-028..031:

- **Reverse-resolution seam relocated.** The single-implementation
  `dispatch.Resolver` interface was an artificial seam; the reverse lookup is now
  a named `pointlist.ReverseResolver` (its only home), and Dispatch consumes it
  via alias instead of redeclaring it.
- **Admin API constructors consolidated.** Six `New*` constructors (auth × feature
  matrix) collapsed to two — `NewServer` (no-auth) and `NewSecureServer` (JWT via
  `JWTConfig`) — over one shared builder. Optional sources stay in `ServerOptions`.
- **HealthMonitor split into two seams.** `GatewayMetrics` (host uptime/mem/disk)
  and `ConnectorProber` (container liveness via the Docker daemon) are now
  independently testable; `Snapshot` composes them.

## Goal

Turn shallow, copy-pasted, or untestable seams into **deep modules** — high leverage behind a small interface, with the interface as the test surface. The behaviors that the ADRs describe (best-effort store-and-forward, the Normalizer as the single home of identity/semantic mapping, the connector SDK with a bounded ack window, the Point List as one synced concept) currently live as emergent properties of `cmd/gateway/main.go` wiring or as logic welded to NATS/gRPC. This epic concentrates each into one owned, tested module. No behavior change to the wire contract; the gateway↔Building OS gRPC contract stays stable.

This epic also closes a concrete gap surfaced while reviewing `gutp-building-os-oss` #224 (now merged, PR #253/#255/#257): the real gateway-scoped Point List API exists and is **ETag-based** (`GET /gateways/{gatewayId}/pointlist`, `If-None-Match`/304, `?since={etag}` diff, mTLS-derived identity), and Building OS now pushes `EgressDown.point_list_update`. The gateway's provisioning client and egress proto were written against an imagined `/version`+`/snapshot` shape and do not yet match.

## Acceptance Criteria

- [ ] Each deepened module has a small interface that is also its unit-test surface — the targeted behavior is testable in-process, without standing up a live NATS/gRPC/Building OS stack.
- [ ] No regression in the existing live-stack E2E suite (`test/e2e/`, `integration/`).
- [x] No breaking change to `proto/` (Buf breaking-change detection passes); `EgressDown` only gains the additive `point_list_update` field already present on the Building OS side.
- [x] The gateway's HTTP provisioning client speaks the real #224 contract (ETag/304/`?since=`) — `provisioning.HTTPClient` (#58). *Residual:* act on the `point_list_update` push by revalidating (tracked under FEAT-031).

## Child Features

- [x] **FEAT-028: Connector SDK + shared wire contract** (deepens EP-002, fulfils ADR-0005's SDK clause). **Done** — `connector/sdk` shared wire types + `CommandDedup` (#61); `dispatch` consumes `sdk.WriteReply`/`sdk.WriteRequest` as the single definition.
  The Command Channel `control_id` dedup + in-flight sentinel + bounded-ack-window publish are currently re-implemented three times (Go/Python/Java) and already diverging (MQTT: no eviction, replies `in_flight`; BACnet/OPC-UA: 1000-entry evict, silent drop). The wire types (`CommonEvent`, `ControlCommand`, `WriteReply`) are redefined per language with nothing enforcing agreement. Make the Connector↔Gateway internal protocol one generated contract (protobuf or single JSON schema) and give each language a thin SDK owning publish-with-ack-window + dedup behind a small interface, so a Connector carries only protocol-specific code.

- [x] **FEAT-029: Store-and-Forward delivery policy as a tested module** (deepens EP-003, makes ADR-0002 testable). **Done** — `uplink.Forwarder` behind the `FrameSink` seam; gRPC is the `grpcSink` adapter.
  The ADR-0002 rules — advance cursor on `StreamAck.accepted`, record `accepted < sent` as a per-`point_id` drift counter, replay the whole un-acked batch on pre-ack failure, never resend rejects — live entirely inside the untested 95-line `internal/uplink/ingress.go` `runStream`, fused with gRPC stream open/close and two timers. Extract the checkpoint/advance/drift decision behind a small interface over the `storeforward.Buffer` and an injected "send batch → accepted-count" seam; gRPC streaming becomes an adapter at that seam.

- [~] **FEAT-030: Normalizer Common Event → Telemetry decision as a pure module** (deepens EP-003, makes ADR-0001 explicit). **Pure decision done** — `Normalize` is a pure `(Common Event, Resolver) → Outcome{frame|poison|miss}` with a public `Outcome` type (#60). *Residual (tracked: #69):* extract the JetStream consumer (stream/consumer/subject creation, `Fetch`/`Ack`/`Term`) out of the `Normalizer` constructor into a thin adapter behind an `EventSource` seam, so normalizer behavior is testable without a live JetStream.
  The identity/semantic mapping (decode, resolve `local_id`→`point_id`, classify `ok`/`poison`/`miss`, coerce `value` to numeric per CONTEXT.md) is a private function + private enum welded to JetStream `Fetch`/`Ack`/`Term`; it is testable today only via 500 ms channel-timeout inference and forces serial tests through process-global counters. Extract `(Common Event, Resolver) → Outcome{frame|poison|miss}` as a pure module; the JetStream consumer becomes a thin adapter mapping outcome → Ack/Term + counter.

- [~] **FEAT-031: Point List as one deep module, aligned to the real #224 API** (deepens EP-006). **P0 closed.**
  **P0 gap closed** — `provisioning.HTTPClient` already speaks the real #224 ETag contract (`GET /gateways/{gatewayId}/pointlist`, `If-None-Match`/304, `?since=` full/delta) (#58); the imagined `/version`+`/snapshot` client is gone. The reverse-resolution seam is now `pointlist.ReverseResolver` (no longer redeclared in `dispatch`). *Residual (refactor, not P0; tracked: #70):* unify `pointlist.SyncedResolver` + `pointsync.Loop` + the `main.go` bootstrap/first-sync sequencing into one owning module whose interface is the convergence lifecycle, and act on `EgressDown.point_list_update` as a revalidation hint.
  Today "the Point List" is split across `pointlist.SyncedResolver` (atomic swap/persist), `pointsync.Loop` (poll cadence), `dispatch`'s separately-redeclared reverse `Resolver` interface, and `main.go` (initial Load + blocking first sync) — no module owns convergence. Give it one owning module whose interface is the convergence lifecycle (file bootstrap → provisioning sync override → blocking-first-load → forward + reverse resolution).

## Follow-up improvements (from the deepening review)

Non-blocking efficiency items surfaced once the seams were isolated — each is now
a clean, contained follow-up rather than a tangle:

- **#71** — `uplink.Forwarder` polls the buffer every 50 ms; replace with a
  write-notify signal (idle busy-poll + first-frame latency).
- **#72** — `/health` does a blocking disk `statfs` on the Admin API hot path via
  `GatewayMetrics.Sample`; sample periodically and cache.

## Dependencies

- **Cross-repo (now satisfied):** `gutp-building-os-oss` #224 is merged. The real contract is: `GET /gateways/{gatewayId}/pointlist` (ETag/304/`?since=`), `EgressDown.point_list_update` push, mTLS-derived `gateway_id` header binding. FEAT-031 implements against this, not the fixture-file placeholder EP-006 assumed.
- **ADRs (fulfilled, not re-litigated):** FEAT-028→ADR-0005 (connector SDK clause); FEAT-029→ADR-0002; FEAT-030→ADR-0001; FEAT-031→ADR-0003 + ADR-0007 (mTLS identity).
- **Consequence, not a candidate:** `cmd/gateway/main.go` (288 lines, seam-less manual wiring) shrinks as 028–031 land; it is fixed incrementally, not as its own feature.
