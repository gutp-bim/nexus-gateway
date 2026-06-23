# Vision — nexus-gateway

## Why we build this

Buildings run on heterogeneous equipment that speaks incompatible protocols (BACnet, OPC-UA, MQTT, Modbus, and more), each with its own data model, units, and quality semantics. Building OS needs one clean, normalized stream of telemetry and one reliable command path — not N protocol-specific integrations. nexus-gateway is the edge component that absorbs this diversity and presents Building OS with a single stable gRPC contract.

`nexus-gateway` exists so that:

- Building OS receives normalized, common-model telemetry regardless of the underlying field protocol.
- New protocols can be added by dropping in a connector container — without touching the core or the Building OS contract.
- Edge sites keep operating through network outages via local store-and-forward buffering.

## Success looks like

- A building operator adds a new protocol by deploying one connector container; no core changes, no gRPC contract changes.
- Telemetry from BACnet, OPC-UA, and MQTT devices arrives at Building OS in identical common-model form, with unified point/device IDs, units, timestamps, and quality.
- During an uplink outage, data is buffered locally in SQLite and forwarded in order once connectivity returns, with no loss.

## Out of scope

- Kubernetes-based orchestration (the runtime is container-based but not K8s-dependent).
- Multi-gateway federation across sites.
- Firmware OTA update infrastructure. (Scope change 2026-06-13: **connector** OTA update — signed OCI images distributed via the Connector Catalog — is now IN scope; see ADR-0006 / EP-007.)
- Semantic reasoning, knowledge graph, or inference engines.
- Device registry, metadata registry, and command authority — these are owned by Building OS as the System of Record.
- Reimplementing EdgeX Foundry / Eclipse Hono capabilities that duplicate Building OS responsibilities.
