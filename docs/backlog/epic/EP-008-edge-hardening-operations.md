# EP-008: Edge Hardening & Operations

**Status:** Prod
**Priority:** P1 (security items P0 before any non-PoC deployment)

## Goal

The pipeline is a working walking skeleton, but several seams are PoC-grade and must be hardened before the gateway runs outside a lab: the northbound/southbound gRPC links are unauthenticated and unencrypted, the in-process sim Connector violates the connector-isolation invariant (ADR-0001), the Normalizer silently drops Telemetry whose `local_id` is not yet in the Point List, and there is no operator-facing run book. This epic closes those gaps so the system is **operable, observable, and secure-by-default** without changing the pipeline shape.

This epic does **not** introduce new pipeline stages; it hardens existing ones. Connector distribution/signing is EP-007; this epic stops at how the gateway *runs and connects*.

## Acceptance Criteria

- [ ] The Ingress uplink (`internal/uplink`) and the Egress agent (`internal/egress`) connect to Building OS over TLS with a verified server certificate; `insecure.NewCredentials()` is gone from non-test code.
- [ ] The gateway presents a **client certificate** whose CN/SAN encodes its `gateway_id` for mTLS terminated at the Building OS Envoy edge (ADR-0007); no OIDC bearer token is added to the gRPC link (Building OS does not validate one there). A config-driven `--insecure` h2c path exists only for dev/CI behind an explicit flag.
- [ ] Transport security degrades safely: a cert/token error surfaces as a connection failure that the Store-and-Forward buffer rides out (ADR-0002), never as silent data loss or a crash loop without backoff.
- [ ] The Normalizer distinguishes a **poison** Common Event (unparseable/permanently invalid) from a **point-list miss** (unknown `local_id`): both are `Term()`-ed (no pointless `Nak ×3` redelivery), each under its own metric (`normalizer_invalid_total`, `normalizer_unresolved_total{reason="point_list_miss"}`) so misses are observable. Drop-on-miss is acceptable under best-effort (ADR-0002): the Point List is near-static, synced before the pipeline accepts telemetry, so a miss signals genuine misconfiguration, not a timing race — no parking/hold machinery is built.
- [ ] Point List sync cadence is **initial-sync-then-slow-poll** (default ~10 min, configurable, may be slower) rather than the current 30 s — the list rarely changes, so frequent polling is waste (ADR-0003).
- [ ] The sim Connector no longer runs in-process by default: it is gated behind an explicit `--dev-sim` flag, **off by default**, documented as a zero-dependency dev/smoke path superseded by the EP-009 simulators. The connector-isolation invariant (ADR-0001) holds in the default build (no in-process connector).
- [ ] A CI workflow exists (none today) that runs `go build`/`go test` on the pinned Go 1.25.x; `go.mod` keeps `go 1.25` (stable, matches the local/CI toolchain) with an explicit `toolchain` directive — the original "downgrade to stable" concern is moot since 1.25 is stable.
- [ ] A top-level `README` documents: what the gateway is, the pipeline diagram, prerequisites, `docker compose up` quickstart, the env/flags surface (NATS, BOS, Keycloak, Admin API), and how to point connectors at the simulators (EP-009).
- [ ] `docker compose up` from a clean checkout brings the full stack (NATS, mock/real Building OS, gateway, Keycloak, Admin UI) to healthy with documented health checks; no manual post-steps.

## Child Features

- [ ] FEAT-032: gRPC transport security — config-driven TLS/mTLS (CA + client cert/key, CN/SAN = `gateway_id`) for Ingress uplink and Egress agent; default-secure with explicit `--insecure` dev path (ADR-0007). External dep: Building OS Envoy/cert-manager edge (#161).
- [ ] FEAT-033: Normalizer drop-and-meter policy — `Term()` poison + miss separately with per-reason metrics (replace `Nak ×3`); slow the Point List sync to initial + ~10 min poll. (No ADR — drop-on-miss is ADR-0002 best-effort applied; requirements confirmed relaxed.)
- [ ] FEAT-034: Sim Connector de-default — gate in-process `sim` behind `--dev-sim` (off by default); default build has no in-process connector (ADR-0001 isolation). Not containerized — superseded by EP-009 real simulators.
- [ ] FEAT-035: CI + toolchain hygiene — stand up the CI workflow (build + unit test on Go 1.25.x), add `toolchain` directive, `go mod tidy`. (go.mod stays at 1.25; real gap is absence of CI.)
- [ ] FEAT-036: Operator run book & compose hardening — README, quickstart, env/flags reference, health-checked one-command bring-up.

## Dependencies

- EP-001 Core Agent — FEAT-034 reuses the Docker SDK lifecycle Manager to host the externalized sim.
- EP-003 Normalizer/Uplink — FEAT-032 and FEAT-033 modify these stages.
- EP-005 Control Path — FEAT-032 also secures the Egress gRPC link used by the Command path.
- **External:** Building OS (`gutp-building-os-oss`) must accept the chosen TLS + credential mechanism; FEAT-032 is blocked on that contract being agreed in ADR-0007.
- **Resolved (grill 2026-06-13/14):**
  - ADR-0007 written — mTLS at the Building OS Envoy edge, h2c in-cluster, `gateway_id`↔cert CN/SAN, no OIDC token on the gRPC link. FEAT-032 is AFK on the gateway side (config + TLS dialer); production cutover waits on Building OS edge (#161, external).
  - Point-list-miss policy: **no ADR** — relaxed loss/timing requirements collapse the trade-off to drop-and-meter under ADR-0002; FEAT-033 is AFK. Sync cadence relaxed to initial + ~10 min.
- **No remaining HITL gates.**
