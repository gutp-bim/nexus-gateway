# Background, positioning & technical challenges

*English / [日本語](background.ja.md)* — top: [README](../README.md)

This document explains *why* nexus-gateway exists, *how* it is positioned
relative to adjacent systems, and *what* technical challenges it faces — grounded
in the design decisions (ADRs) and a comparison with other edge/IoT platforms.
For how to run it see the [README](../README.md); for individual decisions see
the [ADRs](adr/); for vocabulary see [CONTEXT.md](../CONTEXT.md).

---

## 1. Positioning

nexus-gateway is **not** merely "a BACnet / OPC-UA / MQTT gateway." It is best
understood as the **edge-side protocol-absorption layer that lets Building OS be
the System of Record**.

Building OS owns the canonical record of equipment, points, and the Digital Twin;
nexus-gateway is responsible only for *connection and translation*. The essence
of the system is to **converge the diversity of building-equipment protocols into
Building OS's single telemetry/control contract keyed by `(gateway_id, point_id)`**.

A key point is *why a general-purpose IoT edge platform like EdgeX Foundry is not
adopted wholesale*: EdgeX's Core Metadata / Core Command duplicate the Digital
Twin, device registry, and command path that Building OS already owns. So
nexus-gateway leans toward a **lightweight, Azure-IoT-Edge-style containerized
connector manager + gRPC uplink**.

---

## 2. Why this system is needed

Modern smart buildings increasingly need to handle diverse equipment data —
HVAC, lighting, power, environmental sensors, access control, robots, external
IoT — across the board. But field equipment is tightly bound to protocols
(BACnet, OPC-UA, Modbus, MQTT), vendor-specific addressing, and per-BMS naming
conventions. If upper-layer applications dealt with these directly, **per-building,
per-vendor bespoke implementations** would be unavoidable.

### 2.1 Protocols, addressing, and semantics are fragmented

Multiple protocols coexist, each with its own addressing and semantics. Building
OS wants a single telemetry/control contract keyed by `(gateway_id, point_id)`,
so a layer that absorbs protocol differences is needed at the edge.

This problem also exists in general IoT. W3C WoT, for instance, is designed to
*complement* existing standards to improve interoperability rather than replace
them. nexus-gateway is not a WoT runtime, though — its operative contract is
Building OS's Point List / Digital Twin / gRPC contract (the WoT-style
abstraction is conceptually adjacent, but the goal differs).

### 2.2 The gateway must not own a registry

Building OS is the System of Record; the Digital Twin owns equipment, points,
metadata, and control authority. The gateway connects over gRPC and puts only
`(gateway_id, point_id, value, timestamp)` on the wire.

If the gateway kept its own device/point registry, it would be **dual maintenance**
against the twin — breeding inconsistencies in point names, units, writeability,
spatial association, and authorization. So nexus-gateway is strict about
"understands field protocols, but is never the source of truth for the building's
semantics."

### 2.3 Avoiding vendor lock-in — separate the *contract* and *connectors* from any product

Azure IoT Edge runs modules as Docker-compatible containers at the edge, with a
runtime managing install / update / monitoring / communication — conceptually
close to nexus-gateway's "distribute and update signed OCI connectors." But
adopting Azure IoT Edge wholesale ties you to an Azure-IoT-Hub-centric cloud
control plane. nexus-gateway instead uses Connector Catalog, OCI images, cosign,
the Docker Engine API, gRPC, and NATS for a cloud-agnostic setup — GHCR for MVP,
extensible to Harbor / ECR / ACR / Artifact Registry / air-gapped sites.

---

## 3. Comparison with similar systems

| System | Summary | Difference from nexus-gateway |
|--------|---------|-------------------------------|
| **EdgeX Foundry** | General IoT edge platform: Device Service, Core Metadata, Core Command, App Service, Security | Rich, but Core Metadata/Command duplicate Building OS's Twin/command path. We borrow only the Device-Service-style separation; no registry or command core |
| **Azure IoT Edge** | Runs Docker-compatible module containers at the edge; runtime handles install/update/monitoring | Container-module model is close, but we run independently via Building OS + Connector Catalog + OCI registry, not an Azure control plane |
| **Eclipse Kura** | Java/OSGi OSS IoT edge framework with device mgmt + industrial protocols | OSGi monolithic-runtime style; differs from per-container connectors aligned directly to the Building OS Point List |
| **Eclipse Hono** | Connects many IoT devices to a protocol-neutral backend API (HTTP/MQTT/AMQP/CoAP) | Close as a large-scale connection backend, but not centered on a building Point List, REC/Brick/QUDT, or native-address resolution |
| **ThingsBoard IoT Gateway** | OSS gateway connecting legacy protocols to ThingsBoard (MQTT/Modbus/OPC-UA/BACnet) | Protocol connectivity is close, but assumes ThingsBoard above. We make the Building OS Twin/Point List the source of truth |
| **EMQX Neuron** | Lightweight IIoT connectivity server converting industrial protocols to MQTT etc. | Strong at MQTT conversion, but our goal is normalization into the canonical telemetry/control contract, not MQTT-ification |
| **OpenRemote** | 100% OSS IoT device-management platform (incl. building management) | Provides a whole platform; we limit scope to an edge integration layer beneath Building OS |

The distinctiveness of nexus-gateway is *not* "building yet another general IoT
platform," but "**implementing the boundary between the Building OS / SBCO data
model and field protocols as a minimal-responsibility edge layer.**"

---

## 4. Relationship to Building OS / SBCO

### 4.1 Building OS

Building OS (OSS Edition) is an OSS smart-building platform that collects HVAC,
power, and environmental data over MQTT / NATS, with a Digital Twin on OxiGraph,
Keycloak auth, OpenTelemetry, and NATS JetStream. Against it, nexus-gateway:

1. talks to field equipment;
2. emits native addressing as Common Events;
3. resolves `local_id → point_id` via the Point List;
4. normalizes into `(gateway_id, point_id, value, timestamp)` TelemetryFrames;
5. streams them to Building OS over gRPC;
6. translates control commands from Building OS into the relevant protocol's write.

(`Connectors → NATS JetStream → Normalizer → Store-and-Forward → gRPC → Building OS`)

### 4.2 SBCO / Point List — responsibility split

| Layer | Responsibility |
|-------|----------------|
| **SBCO / smartbuilding_datamodel_builder** | Authoring/editing/standardizing the Point List & data model: points, equipment, space, units, writeability, native addresses |
| **Building OS** | Source of truth for the Digital Twin & Point List: `point_id` ownership, authorization, history, API, UI, analytics |
| **nexus-gateway** | Field connection & translation. Syncs the Point List, converts native addresses to canonical `point_id`. **Never edits the Point List** |

The Point List's source of truth is the **Building OS twin (OxiGraph
`sbco:PointExt`)**; the gateway only polls a version token, fetches the snapshot,
and converges by diff ([ADR-0003](adr/0003-point-list-source-of-truth.md)). This
boundary keeps Building OS running on the same `point_id` contract even as vendor
equipment or field protocols change.

---

## 5. Technical challenges

### 5.1 Where ID resolution lives

The biggest design point is to **keep `local_id → point_id` resolution out of the
connector** ([ADR-0001](adr/0001-telemetry-pipeline-shape.md)). Connectors emit
Common Events carrying only `local_id` and a native device ref; the Normalizer
joins the Point List to assign `point_id`. This decouples connectors from the
Building OS domain model — the BACnet connector knows only BACnet, the OPC-UA
connector only OPC-UA — and confines REC/Brick/QUDT semantics to the Normalizer.

### 5.2 Why JetStream sits before the Normalizer

If the `local_id → point_id` mapping or units change later, you cannot
reinterpret data when only normalized telemetry remains. Keeping raw Common
Events in JetStream allows **re-normalization (replay)** after a Point List edit
([ADR-0005](adr/0005-jetstream-topology-bounded-replay.md)). Building point lists
always churn at commissioning (names, units, BACnet instance, OPC-UA NodeId,
location, writeability get corrected), so a replay window is an operational safety
valve.

### 5.3 Dropping exactly-once for best-effort

Telemetry delivery is best-effort; strict ordering and exactly-once are non-goals.
A bounded SQLite ring buffer drops the oldest readings when full
([ADR-0002](adr/0002-best-effort-store-and-forward.md)). Building telemetry is
second-to-minute time-series where current state, outage recovery, and
operational continuity matter more than perfect retention of old values. Chasing
exactly-once would drag in frame ids, unique constraints, and dedup, greatly
raising MVP complexity.

### 5.4 Not persisting control is the safe choice

While telemetry may be buffered, **Control Commands are not persisted**. Control
is real-time-or-fail: to avoid a stale command being applied to physical equipment
after recovery, commands ride core-NATS request-reply with a deadline rather than
JetStream ([ADR-0004](adr/0004-control-path-reliable-within-window.md)). Delayed
application of HVAC/lighting/valve/start-stop control causes safety, comfort, and
equipment-protection problems, so the **asymmetric design** — best-effort buffered
telemetry, no stale-write control — is correct.

### 5.5 Connector distribution & update security

Connectors are signed OCI images run with digest pinning, cosign verification, a
registry allowlist, mandatory SBOM/vulnerability scanning, and
stop → replace → health check → rollback
([ADR-0006](adr/0006-connector-distribution-signed-oci.md)). Because connectors
attach to physical equipment, running arbitrary containers would compromise the
control authority itself. Digest pinning (not tags), mandatory signatures, and
running only catalog-listed images is sound supply-chain security.

### 5.6 mTLS and gateway identity

The gateway↔Building OS gRPC is mTLS-terminated at Building OS's Traefik edge, with
`gateway_id` bound to the client certificate's CN/SAN; the gRPC services
themselves speak h2c and delegate cert verification to the edge proxy
([ADR-0007](adr/0007-transport-security-mtls-at-edge.md)). Keycloak/OIDC is scoped
to the human-facing Admin API; machine-to-machine auth is mTLS. For a
long-running, unattended, possibly air-gapped gateway with cert rotation, mTLS is
more natural than OIDC bearer tokens.

---

## 6. Vendor lock-in avoidance

What makes nexus-gateway lock-in-resistant:

1. **Protocol handling confined to independent connectors** — BACnet=BACpypes3,
   OPC-UA=Eclipse Milo, MQTT=Eclipse Paho; each an independent container with no
   Building OS domain model.
2. **A clear connector contract** — read only native addresses from the Point
   List, emit Common Events on `evt.<protocol>.<connector_id>`, receive control on
   `cmd.<protocol>.<connector_id>`.
3. **Standard, replaceable technology** — OCI images, Docker Engine API, cosign,
   NATS, gRPC, mTLS, vendor-neutral OPC UA / BACnet.

### The new lock-in to watch for

Cloud lock-in can be avoided, but a **project-specific API lock-in** can arise. If
Building OS's `gatewaybridge` gRPC contract, the Point List provisioning API, and
the Connector Catalog manifest schema are not published and stabilized, third-party
connector authors become strongly coupled to the Building OS implementation. To
avoid this:

1. Fix the Connector SDK / manifest schema / Common Event schema as **published
   specs**.
2. Stabilize and version the Point List schema as SBCO.
3. Provide a connector **conformance test**.
4. Standardize E2E verification with BACnet / OPC-UA / MQTT simulators.
5. Document a **compatibility policy** for the Building OS gRPC contract.
6. Keep the Connector Catalog from becoming a single-operator black box.

---

## 7. Future priorities

| Priority | Item | Why |
|----------|------|-----|
| **High** | Finalize the Point List provisioning API | [ADR-0003](adr/0003-point-list-source-of-truth.md) requires a new API; without it Building OS cannot be the source of truth (Building OS #224) |
| **High** | Specify the connector contract | Whether third parties can write connectors is the core of lock-in avoidance |
| **High** | Confirm the mTLS edge topology | [ADR-0007](adr/0007-transport-security-mtls-at-edge.md) names Building OS Traefik / cert-manager / CN-SAN binding as an external dependency (Building OS #161) |
| Med | Conformance test / simulator E2E | Verify connector compatibility with the sibling simulators + Building OS mock ([fixtures/integration](../fixtures/integration/README.md), `test/e2e`) |
| Med | Admin UI authz & audit log | The Admin UI drives connector lifecycle, so it needs action auditing, RBAC, and rollback history |
| Med | Catalog governance | Publish the manifest schema and signing policy so the Connector Catalog does not become a new centralization/lock-in point |
| Low–Med | WoT TD / JSON-LD interop | The runtime contract is the Building OS Point List, but generating WoT TDs is useful for external publication/interop |

---

## 8. Summary

nexus-gateway is an **edge integration gateway** that connects the fragmented
protocols of building equipment to Building OS's common data model. It combines
per-protocol connectors, a Normalizer, Store-and-Forward, a gRPC uplink, and
control dispatch to make the boundary between field equipment and Building OS
explicit.

Its defining trait is making Building OS the System of Record and limiting the
gateway to connection and translation. The Point List's source of truth is the
Building OS twin; the gateway resolves `local_id → point_id` from a diff-synced
copy. This avoids a second device registry / command service and concentrates the
consistency of data model, authorization, and control contract in Building OS.

It is not a general IoT platform like EdgeX Foundry, Azure IoT Edge, Eclipse Kura,
Eclipse Hono, ThingsBoard, EMQX Neuron, or OpenRemote, but a **lightweight
protocol-absorption layer specialized to the Building OS / SBCO data model**. It
borrows EdgeX's Device-Service-style separation and Azure IoT Edge's container
module lifecycle while leaving upper responsibilities like Core Metadata / Core
Command to Building OS.

The technical challenges are ID resolution, replayable raw-event retention,
best-effort telemetry, no stale commands, signed-OCI connector distribution, mTLS
gateway identity, and stabilizing the Point List provisioning API. The
**asymmetric design** — tolerating telemetry loss while keeping control
non-persistent and real-time-or-fail — is appropriate for a building control
system.

OSS libraries, standard protocols, OCI images, gRPC, NATS, mTLS, and a shared
Point List make it lock-in-resistant; but if the Connector Catalog, provisioning
API, and gatewaybridge gRPC contract remain closed, a **project-specific API
lock-in** replaces cloud lock-in. The priority going forward is to publish the
Connector SDK, the various schemas, and a conformance test as open specifications.
