# EP-004: Admin UI

**Status:** Prod
**Priority:** P1

## Goal

The Admin UI is the operator console for the gateway: it surfaces gateway/connector/device/telemetry/log state and drives connector lifecycle actions through the Admin API. It makes the gateway operable by humans at a building site.

## Acceptance Criteria

- [ ] Built with React, Next.js, shadcn/ui, and TanStack Table; authenticated via a pluggable provider — **Basic auth by default** (single-site/local install, no external IdP dependency) with **Keycloak as an opt-in** for multi-site/SSO deployments (see FEAT-046; supersedes the earlier Keycloak-only requirement).
- [ ] Gateway Dashboard shows gateway status, uptime, CPU, memory, disk.
- [ ] Connector management lists connectors with version + status and offers Start/Stop/Restart/Upgrade.
- [ ] Device management shows Devices and Points from the synced Point List (EP-006), grouped by protocol/connector.
- [ ] Telemetry monitor shows received/sent/accepted counts, the per-`point_id` drift counter (`accepted < sent`, EP-003), Store-and-Forward buffer depth, EVENTS stream usage, and uplink latency.
- [ ] Log monitor shows connector logs, gateway logs, errors, and warnings.
- [ ] A first-run Onboarding screen explains what the gateway is and walks a new operator through initial setup (see FEAT-047).
- [ ] A User settings screen lets the logged-in operator manage their own account/preferences (see FEAT-048).
- [ ] A Telemetry live feed shows individual point values as they arrive (consumed from NATS), not just aggregate drift counters (see FEAT-049).

## Child Features

- [ ] FEAT-016: Gateway Dashboard
- [ ] FEAT-017: Connector management screen
- [ ] FEAT-018: Device management screen
- [ ] FEAT-019: Telemetry monitor
- [ ] FEAT-020: Log monitor
- [ ] FEAT-046: Local-first authentication — Basic auth as the default NextAuth provider (no external IdP required for a single local install); Keycloak becomes an opt-in provider selected via config/env, not the only option. Admin API side already supports an auth-free/JWKS-optional dev mode (`KEYCLOAK_JWKS_URL`); this closes the gap where the Admin UI (`admin-ui/lib/auth.ts`, `middleware.ts`) hardcodes `KeycloakProvider` and forces every install through an IdP even when running as a single local system.
- [ ] FEAT-047: Onboarding screen — first-run explanation of what the gateway/Admin UI does and a guided walkthrough (connect a connector, confirm telemetry is flowing) for operators with no prior context.
- [ ] FEAT-048: User settings screen — account/profile management for the logged-in operator (password change under Basic auth, display preferences); scope narrows if/when Keycloak account management is used instead.
- [ ] FEAT-049: Telemetry live feed — consume telemetry directly from NATS (or an Admin API endpoint backed by it) and render arriving point values as a live, filterable log/stream, complementing the existing aggregate-only Telemetry monitor (FEAT-019) and connector-process Log monitor (FEAT-020).
