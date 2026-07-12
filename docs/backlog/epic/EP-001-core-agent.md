# EP-001: Core Agent & Connector Lifecycle

**Status:** Prod — backlog audited & closed 2026-07-12 (residuals/deferred items marked inline)
**Priority:** P0

## Goal

The Core Agent is the Go orchestration brain of the gateway. It manages connector containers (start/stop/restart/upgrade), holds configuration, monitors health, provisions gateway-internal infrastructure (NATS JetStream), and exposes the Admin API. Without it there is no way to operate or observe the gateway, so it is the foundation of MVP-1.

(The Egress control-path client also lives inside the Core Agent but is tracked as EP-005.)

## Acceptance Criteria

- [x] Core Agent runs as a single Go binary and manages connector containers via the Docker Engine SDK.
- [x] Core Agent provisions the `EVENTS` JetStream stream on bring-up with ADR-0005 limits (maxAge 48 h, maxBytes 2 GB, DiscardOld — all configurable via `EVENTS_MAX_AGE` / `EVENTS_MAX_BYTES`).
- [x] Connector Registry tracks installed connectors and their versions.
- [x] Lifecycle Manager supports Start, Stop, Restart, and Upgrade of connectors. (Production upgrades are catalog-driven and signature-verified — EP-007 builds on this.)
- [~] Config Manager persists and distributes gateway + connector configuration. — *Partial: distribution works (flags/env for the gateway, env injection at container create for connectors, point-list persistence); a persistent Config Manager component is **deferred** — config source of truth stays env/flags + catalog, and the in-memory registry is rebuilt on restart.*
- [x] Health Monitor reports gateway uptime, CPU, memory, disk, and per-connector liveness.
- [x] Admin API exposes the above operations, protected by Keycloak OIDC/OAuth2.

## Child Features

- [x] FEAT-001: Connector Registry (`internal/lifecycle/registry.go`)
- [x] FEAT-002: Lifecycle Manager (Docker SDK) (`internal/lifecycle/manager.go`)
- [~] FEAT-003: Config Manager — *deferred: see the Config Manager acceptance criterion above.*
- [x] FEAT-004: Health Monitor (`internal/lifecycle/health.go` + `evaluate.go`)
- [x] FEAT-005: Admin API + Keycloak auth (`internal/adminapi`: JWKS validation + operator/viewer RBAC)
