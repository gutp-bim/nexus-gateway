# Point List source of truth is the Building OS twin; the gateway syncs by diff

**Status:** accepted — 2026-06-13

The shared Point List (maps `point_id` ⇄ native addressing, unit, writeability, control schema, device/spatial grouping) has its **single source of truth in the Building OS twin** (OxiGraph `sbco:PointExt`). The gateway holds a *synced copy*: it polls a cheap version token, fetches the authoritative snapshot when it changes, **diffs** against its local copy, and reconverges the Normalizer mapping and Connector poll lists. The gateway never authors the Point List.

We chose Building-OS-as-master because the contract is point-id based (#181) and the divergence between the gateway's copy and the twin is exactly what surfaces as `accepted < sent` telemetry drift (see [0002](./0002-best-effort-store-and-forward.md)). A single authoritative copy with one-directional sync removes the ambiguity of reconciling two editable masters.

## Consequences

- Requires a **new gateway-scoped, versioned Point List provisioning API** on Building OS — the existing `GET /points` is user-RBAC-scoped and omits native addressing. Filed as `gutp-building-os-oss` issue #224.
- Open dependency: whether native addressing (BACnet object/instance, etc.) lives in the twin or in the `smartbuilding_datamodel_builder` CSV must be resolved by Building OS; if Building OS is master, its export must include native addressing. Tracked in #224.
- Local device discovery on the gateway feeds *proposals* into the Point List authoring flow (datamodel builder), not the live twin directly.
- **Sync cadence (clarified, grill 2026-06-14):** the Point List is near-static, so the gateway syncs **once at startup (blocking before the pipeline accepts telemetry) then polls on a slow interval (default ~10 min, configurable, may be slower)** — not the original 30 s. Because telemetry only flows after the initial snapshot is loaded, an unresolved `local_id` downstream signals genuine misconfiguration rather than a sync-lag race; the Normalizer therefore drops-and-meters such events (best-effort, ADR-0002) instead of buffering them.
- **Source precedence (clarified, grill 2026-06-14):** the gateway may bootstrap its Point List from a local **CSV/file** when the provisioning API is unreachable (offline, dev, E2E), but the **authoritative provisioning snapshot always overrides** the file once synced. This is a precedence of *sources*, not a second master: the gateway still never authors. Layering (highest wins): (1) Building OS provisioning sync (#224); (2) local CSV/file bootstrap. Edge-discovered native addressing overriding the twin was considered and **rejected** — it would reintroduce two editable masters; discovery stays proposal-only.
