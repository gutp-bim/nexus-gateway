# EP-013: Dynamic Point List Provisioning Fallback (File ⇄ Building OS)

**Status:** Proposed
**Priority:** P2

## Goal

Make the Point List source precedence described in ADR-0003 ("the gateway may bootstrap its Point
List from a local CSV/file when the provisioning API is unreachable ... but the authoritative
provisioning snapshot always overrides the file once synced") an actual runtime behavior, not just
a startup-time flag choice. Today `--provisioning-url` and `--provisioning-file` are mutually
exclusive: whichever one `cmd/gateway/main.go`'s `switch` picks at process start is used for the
rest of the process lifetime, with no reconciliation if Building OS becomes reachable later. This
epic adds an explicit, opt-in **fallback mode** that starts from the file when Building OS isn't up
yet and promotes to Building OS the first time it answers — while keeping today's file-pinned,
never-touches-BOS behavior available as its own explicit mode, since some deployments (offline
edge sites, dev, E2E/CI) depend on the gateway never dialing out to a provisioning URL at all.

## Current Gap

- `cmd/gateway/main.go:270-298` selects exactly one `provisioning.Client` — `HTTPClient` or
  `FileClient` — in a `switch` evaluated once at startup. `--provisioning-file`'s own flag help
  text already says "overridden by --provisioning-url", but that override is a **static flag
  precedence**, not a live behavior: if `--provisioning-url` is empty, the file is used forever,
  and if it's non-empty, the file is never even read, regardless of whether the URL is reachable.
- `internal/pointsync/loop.go`'s `Loop` is constructed with exactly one `provisioning.Client`
  (`Loop.client`) and has no notion of a primary/secondary source or of switching implementations
  mid-run. Every tick calls the same `l.client.Fetch`.
- If the gateway starts with `--provisioning-file` only, and Building OS becomes reachable later at
  the URL an operator would otherwise have passed via `--provisioning-url`, there is **no code path
  that observes it** (confirmed by code search — no SIGHUP/reload/periodic re-evaluation of the
  provisioning source exists anywhere in `internal/provisioning/` or `internal/pointsync/`). The
  gateway must be restarted with `--provisioning-url` set to pick up Building OS, and at that point
  `--provisioning-file` is silently dropped.
- `internal/provisioning/file.go`'s doc comment already states the *intended* end state — "the
  gateway can sync from a file during dev/E2E and switch to the authoritative API without code
  changes. Per ADR-0003 a provisioning snapshot always overrides any local bootstrap" — but nothing
  in the codebase implements that switch. This epic closes that documentation/implementation gap.

## Scope and Implementation Plan

### 1. Composite fallback client

- Add `provisioning.FallbackClient` in `internal/provisioning/`, implementing the existing
  `Client` interface (`Fetch(ctx, knownETag) (*FetchResult, error)`) by composing an `HTTPClient`
  (primary) and a `FileClient` (secondary) — no changes to either existing implementation's
  public API.
- Before the first successful `HTTPClient.Fetch`, `FallbackClient.Fetch` tries HTTP first; on any
  HTTP error it falls back to `FileClient.Fetch` for that tick and logs that it's running on the
  file source.
- The **first** successful `HTTPClient.Fetch` promotes the client permanently for the rest of the
  process lifetime: from then on `FallbackClient` calls HTTP only and never reads the file again,
  even if a later HTTP call fails. This is a deliberate one-way ratchet (see "Resolved design
  question" below), matching ADR-0003's "once synced" wording and avoiding oscillation between two
  independently-versioned sources.
- The switchover from file-sourced to HTTP-sourced state must force `Full: true` on the returned
  `FetchResult` (a full resnapshot, not an `Added`/`Removed`/`Changed` diff) — the file's SHA-256
  content-hash ETag and Building OS's revision ETag are different namespaces, so `knownETag`
  continuity cannot be assumed across the switch. `Loop.applyDiff` must handle a `Full` result
  correctly replacing an existing non-empty snapshot (this path already exists for the initial
  sync; extend its test coverage to a *mid-run* full replacement).

### 2. Explicit provisioning mode selection

- Add `--provisioning-mode` / `PROVISIONING_MODE` with three values:
  - `file` — pin to `--provisioning-file` only, for the lifetime of the process. `--provisioning-url`
    is ignored even if set (never dialed). **This is the explicit, always-available "don't sync to
    Building OS" mode** the offline/dev/E2E/CI use case requires.
  - `url` — today's URL-only behavior; `--provisioning-file` ignored even if set.
  - `fallback` — the new `FallbackClient` behavior from §1; requires both `--provisioning-url` and
    `--provisioning-file` to be set (fail fast at startup with a clear error if only one is given,
    rather than silently degrading to single-source).
- **Default when `--provisioning-mode` is unset:** reproduce exactly today's behavior byte-for-byte
  (`url` if `--provisioning-url` is non-empty, else `file` if `--provisioning-file` is non-empty,
  else the one-shot `--point-list` fixture bootstrap). This is an additive change — no existing
  deployment's behavior changes unless it opts into `--provisioning-mode=fallback`.

### 3. Observability

- Log a single structured line on every source promotion (`file` → `url`) with the applied
  revision/ETag, so an operator can see in logs exactly when and why the Point List source changed.
- Expose the currently-active provisioning source (`file` vs `url`) as a `/health`-visible field or
  metric, mirroring how `metrics.SetUplinkConnected` already exposes ingress connectivity state —
  useful for confirming a fallback deployment actually promoted to Building OS after a restart.

### 4. Documentation

- Update ADR-0003's "Consequences" section to note the mode split (`file` / `url` / `fallback`) and
  that dynamic override is opt-in, not the default, keeping the ADR's prose in sync with what the
  code actually does (this epic exists precisely because that gap went unnoticed previously).
- Document `--provisioning-mode` in `README.md`/`README.ja.md`'s configuration table and in
  `docs/getting-started.md`/`.ja.md`'s MQTT/AWS IoT section, since a local-CSV-bootstrap-then-BOS
  scenario is exactly what that walkthrough describes.

## Resolved design question

**Should `FallbackClient` fall back to the file again if Building OS goes down *after* an initial
successful sync?** No — once promoted, it stays on HTTP and lets `Loop`'s existing retry/backoff
posture (poll interval + `EgressDown.point_list_update`-triggered revalidate) handle transient BOS
outages, the same way a `url`-only deployment already does today. Reverting to a stale local file
after Building OS has been authoritative risks silently regressing `point_id` mappings the operator
believed were long gone (renamed/removed points reappearing). If this needs to be reconsidered,
open a follow-up rather than expanding this epic's scope.

## Acceptance Criteria

- [ ] `provisioning.FallbackClient` exists, implements `Client`, and is covered by unit tests for:
      HTTP-unreachable-at-start → file bootstrap → HTTP becomes reachable → permanent promotion;
      the promotion `FetchResult` is `Full: true`; a post-promotion HTTP failure does **not** fall
      back to the file.
- [ ] `--provisioning-mode` / `PROVISIONING_MODE` accepts `file`, `url`, `fallback`; an unset value
      reproduces current behavior exactly (existing integration/E2E tests pass unmodified).
- [ ] `--provisioning-mode=file` never opens a connection to `--provisioning-url` even when both
      flags are set — verified by a test that points `--provisioning-url` at a closed port and
      confirms no dial attempt / no error logged about it.
- [ ] `--provisioning-mode=fallback` with only one of `--provisioning-url`/`--provisioning-file` set
      fails startup with a clear, actionable error instead of silently running single-source.
- [ ] A source promotion (file → url) is logged once, structured, and observable via `/health` or a
      metric without requiring log-scraping.
- [ ] `docs/adr/0003-point-list-source-of-truth.md`, `README.md`/`README.ja.md`, and
      `docs/getting-started.md`/`.ja.md` describe the three modes accurately.
- [ ] Integration test: start the gateway with `--provisioning-mode=fallback` against a stopped
      mock Building OS provisioning endpoint and a valid `--provisioning-file`; confirm points
      resolve from the file; start the mock endpoint; confirm the resolver's snapshot converges to
      the mock's point list without a gateway restart.

## Child Features

- [ ] **FEAT-057: `provisioning.FallbackClient`** — composite `Client` implementation, one-way
      promotion, forced full resync on switchover, unit tests.
- [ ] **FEAT-058: `--provisioning-mode` flag/env plumbing** — `file`/`url`/`fallback` selection in
      `cmd/gateway/main.go`, backward-compatible default, fail-fast validation for `fallback` with
      an incomplete flag pair.
- [ ] **FEAT-059: Promotion observability and docs** — structured promotion log line, `/health`
      exposure, ADR-0003/README/getting-started updates.

## Dependencies

- ADR-0003 (`docs/adr/0003-point-list-source-of-truth.md`) — this epic implements its stated but
  unimplemented dynamic-override precedence.
- EP-006 (Point List Sync & Shared Resolver) — `FallbackClient` sits behind the same `Loop`/
  `provisioning.Client` seam EP-006 already built; no changes to `pointsync.Loop`'s public API are
  expected.
- Does **not** depend on `gutp-building-os-oss` #224 (the versioned provisioning API) landing —
  this epic is about *which* already-existing client (`HTTPClient` vs `FileClient`) is active and
  when, not about the wire contract either one speaks.

## Out of Scope

- Any change to `HTTPClient` or `FileClient`'s own fetch/diff logic.
- Falling back to the one-shot `--point-list` fixture bootstrap as a *third* fallback tier inside
  `FallbackClient` — the fixture path remains a separate, simpler mechanism for when neither
  `--provisioning-url` nor `--provisioning-file` is configured at all.
- Re-falling-back to file after a successful Building OS promotion (see "Resolved design question").
- Changing MQTT `MQTT_POINTS_FILE` connector-subscription behavior — unrelated to this Point List
  provisioning seam (see `docs/getting-started.md`'s AWS IoT section for that distinction).

## Delivery Order

`FEAT-057 -> FEAT-058 -> FEAT-059`

FEAT-059's documentation updates should land alongside FEAT-058 rather than strictly after it, since
the mode names need to be user-facing and stable before they're documented.
