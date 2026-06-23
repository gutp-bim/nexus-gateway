# EP-009: Simulator-Backed Protocol Integration

**Status:** Prod
**Priority:** P0

## Goal

The BACnet and OPC-UA connectors are tested today against mocks and unit fixtures. Two sibling simulators expose **real protocol stacks** without field hardware:

- `bacnet-sim-gateway` (bbc-sim) — a standard-compliant virtual BACnet B-BC published over **BACnet/IP** (UDP 47808; Who-Is/I-Am discovery needs host networking for broadcast).
- `opcua-sim-gateway` (opcua-sim) — a standard-compliant virtual **OPC UA** server published over `opc.tcp` (TCP 4840).

This epic wires those simulators into a reproducible integration environment and proves the existing connectors end-to-end against them: **real BACnet/IP + real OPC UA → Common Event → JetStream → Normalizer → Store-and-Forward → gRPC Ingress → mock Building OS**, and the reverse **Control Command → Egress dispatch → connector write → simulator state change**. It validates the connectors against conformant stacks (timing, COV, subscriptions, data types, write priorities) that mocks cannot exercise.

```
[bbc-sim  BACnet/IP :47808] ──▶ [nexus BACnet connector] ─┐
[opcua-sim opc.tcp :4840 ] ──▶ [nexus OPC-UA connector] ─┴▶ EVENTS ▶ Normalizer ▶ S&F ▶ gRPC Ingress ▶ mock Building OS
        ▲ WriteProperty / Write/Method  ◀── [connector write handler] ◀── Command Channel ◀── Egress
```

## Acceptance Criteria

- [ ] A committed compose profile (e.g. `docker-compose.integration.yml`) brings up both simulators plus the gateway stack from a clean checkout; BACnet discovery's host-networking requirement is handled and documented.
- [ ] Both simulators are driven from the **same Point List** the gateway syncs from, so native addresses (`local_id`: BACnet object instance, OPC-UA NodeId) line up between simulator and connector with no hand-edited duplication.
- [ ] **BACnet telemetry E2E:** the BACnet connector discovers (Who-Is/I-Am), reads (ReadProperty/RPM) and/or subscribes (COV) to bbc-sim points and publishes Common Events carrying `protocol=bacnet` and native addressing only (ADR-0001); the frames arrive at the mock Building OS Ingress.
- [ ] **OPC-UA telemetry E2E:** the OPC-UA connector browses/reads/subscribes opcua-sim nodes and publishes Common Events carrying `protocol=opcua`; the frames arrive at the mock Building OS Ingress.
- [ ] **Control E2E (both protocols):** a Control Command dispatched through the Egress/Command Channel reaches the connector write handler, performs the protocol write (BACnet WriteProperty with priority; OPC-UA Write/Method Call), and the change is observable back in the simulator's state; the typed ControlResult is returned within the deadline (ADR-0004), idempotent on `control_id`.
- [ ] A value changed inside the simulator (simulator-generated waveform or Admin-UI-set value) is observed flowing through to the mock Building OS, proving the read path reflects live source state, not cached fixtures.
- [ ] CI (per-PR, headless) runs unit tests plus the **OPC-UA telemetry E2E** against opcua-sim (plain TCP:4840, mock Building OS) as the first integration job. The BACnet telemetry E2E runs in CI if the runner can host BACnet/IP networking; otherwise its discovery path is manual and a directed (configured-IP, non-broadcast) read stays in CI. Control E2E and the full SoS run (EP-010) are manual/nightly.
- [ ] The integration run is documented in the README/run book (EP-008 FEAT-036) so a developer can reproduce both telemetry and control paths locally.

## Child Features

- [ ] FEAT-037: Integration compose & shared Point List — both simulators + gateway stack, single source-of-truth address list, host-networking handling for BACnet.
- [ ] FEAT-038: BACnet telemetry E2E against bbc-sim — discovery/read/COV → Common Event → Ingress, asserted end-to-end.
- [ ] FEAT-039: OPC-UA telemetry E2E against opcua-sim — browse/read/subscribe → Common Event → Ingress, asserted end-to-end.
- [ ] FEAT-040: Control-path E2E through the simulators — BACnet WriteProperty + OPC-UA Write/Method round-trip with ControlResult assertion.

## Dependencies

- EP-002 Protocol Connectors (#5 BACnet, #6 OPC-UA) — the connectors under test.
- EP-005 Control Path (#4, #9, #10) — write handlers exercised by FEAT-040.
- EP-006 Point List Sync (#3) — the shared address list both sides derive from.
- EP-008 FEAT-036 — the run book that documents how to launch the integration environment.
- **External:** `bacnet-sim-gateway` and `opcua-sim-gateway` sibling repos provide the simulator images; their published native addresses must match the Point List used here.
