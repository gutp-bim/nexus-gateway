# Connector distribution: catalog-mediated signed OCI images, digest-pinned, stop-replace-rollback

**Status:** accepted — 2026-06-13

Connectors are distributed as **signed OCI images** and the gateway executes them **digest-pinned only** (`image@sha256:…`) — never by tag, because tags move; `:latest`/`:stable` are forbidden in production. The gateway does not browse registries: it polls a **Connector Catalog** — a **standalone management server** (deliberately *not* Building OS: Building OS stays the System of Record for the building domain, while deployment/distribution is an operations concern with its own lifecycle; the cost of one more operated service is accepted) — which serves approved manifests: `name`, `version`, `image`, `digest`, `min_gateway_version`, `permissions` (network/mounts), `signature_required`.

Verification is mandatory and enforced by the Core Agent (cosign):

- an **unsigned image never runs**;
- a **digest mismatch never runs**;
- a **registry outside the allowlist is never pulled from**;
- images that failed SBOM/vulnerability scanning are **never distributed** (enforced CI-side, before the catalog lists them).

Update detection is **poll-only** (catalog version token, same pattern as Point List sync) — the gateway dials out and accepts no inbound connections; push notification can ride the Egress stream later without changing this model.

Replacement is **stop → replace → health check → rollback**, not canary/blue-green: a Connector holds an *exclusive* connection to physical equipment, so running old and new side-by-side would double COV subscriptions, double events, and double device load. A few seconds of telemetry gap is acceptable under the best-effort contract (ADR-0002). On health-check failure the Core Agent automatically rolls back to the previous pinned digest, which is always retained.

GHCR is the MVP registry; Harbor (or ECR/ACR/Artifact Registry) for production and air-gapped sites.

## Consequences

- The whole flow is Azure IoT Edge-like module update, implemented lightweight: Go Core Agent + Docker Engine API + cosign.
- Connector = signed OCI image; Catalog = approved manifests; Core Agent = pull/verify/run/rollback manager; Admin UI = install/update/rollback console.
- The Catalog is a new external dependency with its own availability story; an unreachable Catalog degrades to "no installs/updates", never to "stop running connectors".
- `min_gateway_version` gates compatibility; the Core Agent refuses manifests newer than it understands.
- Catalog `permissions` (network/mounts) become the container-creation contract — connectors get only what their manifest declares.
- Vision scope change: connector OTA is now in scope (firmware OTA remains out).
