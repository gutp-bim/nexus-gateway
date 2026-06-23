# Security Policy

nexus-gateway runs at the building edge and carries the `(gateway_id, point_id)`
identity Building OS treats as authoritative, plus a control path that writes to
physical equipment. We take security reports seriously.

## Reporting a vulnerability

**Please do not open a public issue, PR, or discussion for a security report.**

Report privately through **GitHub's private vulnerability reporting**:

1. Go to the repository's **Security** tab → **Report a vulnerability**
   (this opens a private advisory visible only to maintainers).
2. Include: affected version/commit, a description, reproduction steps or a PoC,
   and the impact you observed.

If you cannot use GitHub advisories, open a minimal public issue asking a
maintainer to contact you privately — **without** any vulnerability detail.

### What to expect

- **Acknowledgement** within a few business days.
- An initial assessment (severity, affected versions) and a planned fix window,
  shared on the advisory thread.
- Credit in the advisory/release notes when a fix ships, unless you prefer to
  remain anonymous.

Please give us reasonable time to release a fix before any public disclosure
(coordinated disclosure).

## Supported versions

This project is pre-1.0 and evolving. Security fixes target the **`master`
branch / latest release**. There is no backported-support window for older tags
yet; if that changes it will be recorded here.

## Security model (what the gateway already enforces)

Reports are most useful when framed against the intended model:

- **Machine link to Building OS — mTLS at the edge (ADR-0007).** The gRPC links
  are authenticated by **mutual TLS terminated at the Building OS Traefik edge**
  (`TLSOption` + `passTLSClientCert`), with the gateway's `gateway_id` bound to
  the client-cert CN (cert-manager-issued). The edge injects a trusted
  `X-Gateway-Id` header from the cert; Building OS enforces it equals the frame's
  `gateway_id` (anti-spoofing, behind a production enforce toggle). The gateway is
  config-driven and default-secure; clear-text h2c is available **only** behind an
  explicit `--bos-insecure`/dev flag. There is no bearer token on these links
  (Building OS does not validate one). The gateway never sends `X-Gateway-Id`
  itself — the edge supplies it, and any externally-supplied value is stripped.
- **Connector distribution — digest-pinned, allowlisted registry (ADR-0006).**
  The intended model is signed, digest-pinned OCI images pulled only from an
  allowlisted registry (`--catalog-allowlist`) and run by `image@sha256:…`
  rather than by tag. **MVP uses a file-backed development catalog; production
  cosign signature verification is the intended model and is tracked as MVP+1**
  — it is not yet enforced by default. The registry allowlist and digest-pinned
  references apply on the catalog path today.
- **Human operator access — Keycloak/OIDC.** The Admin API & UI are protected by
  OIDC with `operator`/`viewer` roles. JWTs are validated against the configured
  JWKS with audience + issuer enforcement.

A report showing any of these guarantees can be bypassed — a non-allowlisted or
tag-mutated (non-digest-pinned) image running, a token/role check evaded, an mTLS/identity
spoof, a control command applied without authority, or a path that leaks
credentials or equipment access — is high-value.

## Out of scope / not vulnerabilities

- **Dev credentials and secrets in the compose stack.** The `docker-compose.yml`
  / `fixtures/keycloak/` values — `operator`/`operator`, `viewer`/`viewer`,
  `admin-ui-secret`, `NEXTAUTH_SECRET=dev-secret-change-in-production` — are
  intentionally weak placeholders for local development. They are documented as
  "change before any non-lab deployment" and are not a vulnerability in
  themselves. Reports that real deployments must replace them are welcome as
  documentation issues, not security reports.
- **`--bos-insecure` plaintext h2c.** This is a deliberate dev/CI affordance for
  environments with no edge proxy; it is off by default. Running it in
  production is a deployment misconfiguration, not a gateway flaw.
- **Vulnerabilities in Building OS, Keycloak, NATS, or Docker themselves.**
  Report those upstream. Issues in *how the gateway integrates with* them are in
  scope.
- Findings that require an already-compromised host or root on the gateway box.

## Hardening for deployment

Operators should, at minimum: replace all dev credentials and secrets; set
`KEYCLOAK_JWKS_URL` (never expose the Admin API auth-disabled); provision real
mTLS material (`--bos-ca`/`--bos-cert`/`--bos-key`) and leave `--bos-insecure`
off; and restrict the allowlist to the registries you trust.
