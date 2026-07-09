# Contributing to nexus-gateway

Thanks for your interest in contributing. This gateway sits at the building edge
between heterogeneous equipment and Building OS, so correctness and a stable wire
contract matter more than feature velocity. This guide gets you productive and
explains the conventions a change must follow.

New here? Start with the **[Getting Started guide](docs/getting-started.md)**,
then come back.

---

## Ground rules

- **Building OS is the System of Record.** This gateway is responsible only for
  *connection and translation* — never device/metadata registry or command
  authority. Changes that pull domain ownership into the gateway are out of scope.
- **The wire contract is load-bearing.** The gateway↔Building OS gRPC contract
  (`proto/`, package `gatewaybridge`) must stay backward-compatible; Buf
  breaking-change detection gates every PR.
- **Decisions live in ADRs.** Don't re-litigate a settled
  [ADR](docs/adr/) in a PR. If you believe one should change, open an issue that
  re-opens it with the new constraint.

---

## Before you start

Read in this order — it mirrors how the project reasons about changes:

1. [README](README.md) — what this is and why.
2. [CONTEXT.md](CONTEXT.md) — the domain glossary. **Use these exact terms**
   (Connector, Common Event, Telemetry, Point List, Normalizer, …) in code,
   tests, comments, and commit messages.
3. [docs/adr/](docs/adr/) — the seven decisions everything rests on.
4. The epic under [docs/backlog/](docs/backlog/) closest to your change.

---

## Development setup

```bash
git clone https://github.com/gutp-bim/nexus-gateway
cd nexus-gateway

# Go toolchain ≥ 1.25 required.
make build        # buf generate + go build ./...
make test         # go test ./...    (use `go test -race ./...` locally)
```

| Task | Command |
|------|---------|
| Regenerate gRPC stubs after editing `proto/*.proto` | `make generate` (runs `buf generate`) |
| Build everything | `make build` |
| Run the Go test suite | `go test -race ./...` |
| Check the proto for breaking changes | `make buf-breaking` |
| Admin UI checks | `cd admin-ui && npm ci && npm run type-check && npm run build` |
| Run the gateway with no equipment | `go run ./cmd/gateway --dev-sim` |

`buf` is expected at `$(HOME)/go/bin/buf` (override with `BUF=...`); install with
`go install github.com/bufbuild/buf/cmd/buf@latest`.

---

## Tests

We practice **behavior-first testing**: exercise modules through their public
interface, not their internals. A good test reads like a specification and
survives an internal refactor. Several modules were deliberately shaped into
small interfaces so they're testable in-process without a live
NATS/gRPC/Docker stack — see the
[module-seams table](README.md#module-seams-testability).

- Add or update tests in the same PR as the behavior change.
- Prefer one focused test per behavior over a single sprawling test.
- **Don't** test through implementation details (private functions, internal
  collaborators, querying the DB directly instead of the interface). If renaming
  an internal function breaks a test, that test was coupled to implementation.
- Live-stack **E2E** tests live in `integration/` and `test/e2e/` (build tag
  `e2e`); they skip automatically without the relevant `E2E_*`/simulator env.
  See [docs/e2e-test-overview.md](docs/e2e-test-overview.md).

---

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/): a
`type(scope): summary` subject. Types in use here: `feat`, `fix`, `refactor`,
`docs`, `test`, `chore`. Examples from history:

```
feat(pointlist): CSV loader for the shared Point List
refactor: deepen four shallow seams (dispatch/adminapi/health/uplink)
docs: add Japanese README with cross-links
test(e2e): BOS GatewayIngressService acceptance test
```

Keep the subject imperative and ≤ ~72 chars; put the *why* in the body.

### Sign-off (DCO)

By contributing you certify the [Developer Certificate of
Origin](https://developercertificate.org/). Sign every commit:

```bash
git commit -s        # appends a "Signed-off-by: Name <email>" trailer
```

---

## Pull requests

1. Branch off `master` (e.g. `feat/point-list-etag`, `fix/soak-timeout`).
2. Keep the PR focused — one logical change. Split unrelated cleanups out.
3. Make sure the CI gates pass locally first:
   - `go build ./...` and `go test -race ./...`
   - `make buf-breaking` if you touched `proto/`
   - Admin UI `type-check` + `build` if you touched `admin-ui/`
4. In the description: what changed, why, which ADR/epic it relates to, and how
   you verified it. Note any wire-contract impact explicitly (there should be
   none unless the PR is specifically about the contract).
5. CI (`.github/workflows/ci.yml`) runs the Go build/test, the proto
   breaking-change check, and the Admin UI build on every PR.

---

## Adding a protocol connector

A connector is an isolated container that holds **no** Building-OS domain model
and no dependency on other connectors. It reads only the native addresses it must
poll from the Point List, publishes Common Events to `evt.<protocol>.<id>`, and
serves Control Commands on `cmd.<protocol>.<id>` (idempotent on `control_id`).
Use `connector/{bacnet,opcua,mqtt}` as templates, package it as a **signed OCI
image**, and list it in the Connector Catalog so the Core Agent runs it
digest-pinned (ADR-0006). The
[extending guide](README.md#extending-add-a-protocol-connector) has the details.

---

## Reporting bugs and proposing features

- **Bugs / features** — open an issue using the templates in
  [`.github/ISSUE_TEMPLATE/`](.github/ISSUE_TEMPLATE/).
- **Security vulnerabilities** — **do not** open a public issue. Follow
  [SECURITY.md](SECURITY.md) (GitHub private vulnerability reporting).

---

## License

By contributing, you agree your contributions are licensed under the project's
[Apache-2.0](LICENSE) license.
