# EP-004: Admin UI

**Status:** Prod
**Priority:** P1

## Goal

The Admin UI is the operator console for the gateway: it surfaces gateway/connector/device/telemetry/log state and drives connector lifecycle actions through the Admin API. It makes the gateway operable by humans at a building site.

## Acceptance Criteria

- [ ] Built with React, Next.js, shadcn/ui, and TanStack Table; authenticated via Keycloak.
- [ ] Gateway Dashboard shows gateway status, uptime, CPU, memory, disk.
- [ ] Connector management lists connectors with version + status and offers Start/Stop/Restart/Upgrade.
- [ ] Device management shows Devices and Points from the synced Point List (EP-006), grouped by protocol/connector.
- [ ] Telemetry monitor shows received/sent/accepted counts, the per-`point_id` drift counter (`accepted < sent`, EP-003), Store-and-Forward buffer depth, EVENTS stream usage, and uplink latency.
- [ ] Log monitor shows connector logs, gateway logs, errors, and warnings.

## Child Features

- [ ] FEAT-016: Gateway Dashboard
- [ ] FEAT-017: Connector management screen
- [ ] FEAT-018: Device management screen
- [ ] FEAT-019: Telemetry monitor
- [ ] FEAT-020: Log monitor
