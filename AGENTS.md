# AGENTS.md

> Operating contract for AI agents working in this repository.

---

## 1. Project Overview

nexus-gateway — a Building OS Integration Gateway: an edge gateway connecting BMS, IoT devices, and equipment protocols to Building OS.

nexus-gateway runs at the building edge to collect equipment data, relay control commands, absorb protocol differences, and normalize everything into a Building OS common data model. It is used by building operators and systems integrators who need to bridge heterogeneous equipment (BACnet, OPC-UA, MQTT, Modbus, …) to a central Building OS over a single gRPC contract. Building OS is the System of Record; this gateway is strictly responsible for connection and translation — never for device/metadata registry or command authority.

Tech stack: Go (Core Agent), Protocol Buffers + Buf, gRPC, NATS JetStream, SQLite, OpenTelemetry. Connectors: Python (BACnet — BACpypes3/BAC0), Java (OPC-UA — Eclipse Milo), Go (MQTT 5.0). Modbus TCP is planned (not yet implemented). Admin UI: React + Next.js + shadcn/ui + TanStack Table. Auth: Keycloak (OIDC/OAuth2). Containerized, non-Kubernetes runtime.

---

## 2. Required Reading

Reading order: **Vision → Memory → ADR → Backlog → Spec → Issue**

Before making architectural decisions:

1. `docs/vision/vision.md` — why this exists and what is out of scope
2. `docs/memory/architecture.md` — system structure and open questions
3. `docs/memory/decisions.md` — settled and pending design choices
4. `docs/adr/` — formal decision records

Before implementing a feature:

5. Relevant `docs/backlog/` item — what and why
6. Relevant `docs/specs/` file — acceptance criteria and API shape
7. `docs/memory/pitfalls.md` — known failure modes

---

## 3. Workflow

1. Read the issue or task.
2. Read the linked spec and backlog item.
3. State your implementation plan before writing code.
4. Implement the minimal change required.
5. Run tests.
6. Report deliverables (see §6).

When requirements are unclear:

- Ask questions.
- Do not assume.
- Do not invent requirements.

---

## 4. Coding Rules

- Prefer the simplest solution that satisfies the spec.
- Avoid premature abstraction.
- Keep dependencies minimal.
- Follow existing patterns in the codebase.
- **Connector isolation:** each protocol is an independent container. Connectors must not depend on one another, and must not hold equipment-specific domain models — they emit the common event format only.
- **gRPC contract stability:** the gRPC API to Building OS is the public contract. Internal implementation may change freely, but Protobuf-defined messages/services must not break. Run Buf breaking-change detection before changing `.proto` files.
- **Semantic mapping lives in the Normalizer**, never in connectors.
- Go: single-binary builds, explicit error handling, context propagation for all I/O.

---

## 5. Scope Control

Only modify files directly related to the task.

Do not:

- Refactor unrelated code
- Rename modules not mentioned in the task
- Reorganize directory structure
- Add documentation unless the task requires it
- Introduce new dependencies without discussion
- Add responsibilities that belong to Building OS (device registry, metadata registry, command authority)

---

## 6. Deliverables

After completing a task, report:

- Files changed and why
- Tests executed and results
- Any follow-up work identified

---

## 7. Safety Rules

Never do the following without explicit confirmation:

- Delete data or files
- Execute database migrations
- Push to `main`
- Deploy to any environment
- Modify production configuration
- Send messages to external services
