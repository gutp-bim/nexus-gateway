# EP-007: Connector Distribution & Secure Update

**Status:** Prod
**Priority:** P0

## Goal

Deliver the safe pull/update path for Connectors (ADR-0006): Connector developers build/test/sign images into a Container Registry; the **Connector Catalog** (standalone management server) lists approved manifests; the Core Agent polls the Catalog, pulls **digest-pinned signed OCI images**, verifies them, and performs stop → replace → health check → rollback. The Admin UI drives install/update/rollback. This supersedes the tag-based upgrade in EP-001 (FEAT-002) as the production update mechanism.

```
Connector developer → build/test/sign → Container Registry
                                            ↓
                                     Connector Catalog
                                            ↓ (poll, version token)
                                     Gateway Core Agent
                                            ↓ pull / verify / stop / replace / rollback
                                     Connector Container
```

## Acceptance Criteria

- [ ] Core Agent runs connectors digest-pinned only (`image@sha256:…`); execution by tag is impossible; `:latest`/`:stable` never reach production.
- [ ] Core Agent polls the Catalog (version token; poll-only, dial-out) and installs/updates only catalog-listed manifests: `name`, `version`, `image`, `digest`, `min_gateway_version`, `permissions`, `signature_required`.
- [ ] cosign signature verification is mandatory: unsigned image ⇒ never runs; digest mismatch ⇒ never runs; registry outside the allowlist ⇒ never pulled.
- [ ] SBOM + vulnerability scan gate runs in CI before an image can be listed in the Catalog.
- [ ] Update flow: detect → pull → verify → stop old → start new → health check → on failure automatic rollback to the previous pinned digest (always retained).
- [ ] Catalog `permissions` (network/mounts) are applied as the container-creation contract; `min_gateway_version` is enforced.
- [ ] Unreachable Catalog degrades to "no installs/updates" — running connectors are never stopped because of it.
- [ ] Admin UI shows per connector: name, version, image digest, signature status, update availability, current status, logs, and offers Install / Update / Rollback.

## Child Features

- [ ] FEAT-028: Catalog client + verified install (digest-pinned pull, cosign verify, allowlist, permissions, min_gateway_version)
- [ ] FEAT-029: Update & rollback engine (poll detect, stop-replace-health-rollback, previous-digest retention)
- [ ] FEAT-030: Connector CI/CD reference pipeline (GitHub Actions: build → test → scan/SBOM → cosign sign → push GHCR → catalog manifest update)
- [ ] FEAT-031: Admin UI Connector Catalog screens (catalog browse, install with config, signature/update status, rollback)

## Dependencies

- EP-001 (#8) Core Agent lifecycle — install/replace builds on the Docker SDK lifecycle manager.
- **External:** the Connector Catalog management server. MVP develops against a reference/file-backed catalog implementation in this repo, interface-compatible with the real service.
- EP-004 (#13) Admin UI shell — FEAT-031 extends it.
- Registry: GHCR for MVP; Harbor/ECR/ACR for production per site.
