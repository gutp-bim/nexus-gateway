# EP-006: Point List Sync & Shared Resolver

**Status:** Prod
**Priority:** P0

## Goal

The Point List's single source of truth is the Building OS twin (OxiGraph `sbco:PointExt`); the gateway holds a synced copy and converges by diffing against the authoritative snapshot (ADR-0003). This epic delivers that sync loop plus the **shared resolver** consumed by both directions: the Normalizer (`local_id` → `point_id`, EP-003) and the control dispatcher (`point_id` → native, EP-005). The gateway never authors the Point List.

## Acceptance Criteria

- [ ] Gateway polls a cheap version token and fetches the authoritative Point List snapshot only when it changes (gateway-scoped, versioned provisioning API — `gutp-building-os-oss` #224).
- [ ] On snapshot change, the gateway **diffs** against its local copy and reconverges: Normalizer mapping reload + Connector poll/subscribe list reload, without restarting components.
- [ ] One shared resolver serves both lookups: `local_id`→`point_id` (Normalizer) and `point_id`→native `(protocol, device/object/instance)` + writeability/control schema (control dispatch).
- [ ] The synced copy survives gateway restart (local persistence) and reconverges on next poll.
- [ ] Point List drift vs Building OS surfaces operationally as the per-`point_id` drift counter on the Ingress uplink (`accepted < sent`, EP-003) — no separate reconciliation protocol.
- [ ] Per `smartbuilding_datamodel_builder`: 1 row = 1 Point, mapping `point_id` ⇄ `local_id`, unit, writeability, control schema, device grouping, spatial context.

## Child Features

- [ ] FEAT-025: Point List snapshot client (version-token poll, snapshot fetch, local persistence)
- [ ] FEAT-026: Diff & convergence engine (Normalizer mapping + Connector poll list reload)
- [ ] FEAT-027: Shared bidirectional resolver (`local_id`↔`point_id`, writeability/control schema lookup)

## Dependencies

- **Cross-repo:** `gutp-building-os-oss` #224 — gateway-scoped, versioned point-list provisioning API on Building OS. Until it lands, develop against a fixture snapshot file with the same schema.
- EP-003 (Normalizer) and EP-005 (control dispatch) consume the resolver.
