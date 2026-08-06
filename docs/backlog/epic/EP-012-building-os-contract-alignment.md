# EP-012: Building OS gRPC Contract Alignment & Typed Telemetry

**Status:** In Progress
**Priority:** P0

## Progress

Implemented on `agent/typed-telemetry-contract`: the vendored/current proto and generated Go code,
typed Common Event and Normalizer, backward-compatible typed SQLite persistence, MQTT/BACnet/OPC-UA
producer support, typed Admin API/UI display, Egress Point List revision heartbeat, semantic sibling-repo
contract tests, Buf lint, and unit/integration coverage. The remaining release gate is running the
environment-dependent SoS assertion against a live `gutp-building-os-ri` stack and verifying the three
types through its hot telemetry persistence/API.

## Goal

Bring nexus-gateway into full source and behavioral compliance with the Building OS-owned
`gatewaybridge` contract in `../gutp-building-os-ri/proto/`. In particular, replace the gateway's
numeric-only Ingress model with the current discriminated telemetry value:

```proto
oneof value {
  double value_num = 3;
  string value_str = 6;
  bool value_bool = 7;
}
```

Field 3 remains the numeric wire field, so deployed numeric senders and receivers stay wire-compatible.
String/status/enum-label and boolean readings become first-class values throughout the gateway instead
of being converted to numeric codes or placed in `attributes`. Building OS remains the contract owner;
nexus vendors and consumes that contract without defining a competing public API.

This epic also closes the current additive Egress schema drift (`EgressUp.status = 3` and
`GatewayStatus.applied_revision`) so both vendored `gatewaybridge` proto files match the current
Building OS schemas, apart from the gateway-local `go_package` code-generation option.

## Current Gap

- `proto/gateway_ingress.proto` and generated Go code still expose `double value = 3` only.
- Common Events, the Normalizer, the recent-value Admin API, and tests assume `float64`.
- Current MQTT work carries a string reading in `attributes["value_text"]`; the canonical contract now
  requires the primary reading in `TelemetryFrame.value_str`.
- The Store-and-Forward buffer serializes the old generated message, so it cannot retain string or
  boolean values across an outage/restart.
- `proto/gateway_egress.proto` lacks the optional gateway-status heartbeat added by Building OS.
- Architecture memory and `CONTEXT.md` still record the superseded "double only / no oneof" decision;
  EP-003 now points to this epic for the required migration.

## Scope and Implementation Plan

### 1. Synchronize the authoritative contract

- Vendor the current Building OS Ingress and Egress schemas, retaining only the nexus-specific
  `go_package` option needed for Go generation.
- Regenerate `gen/gateway_ingress*.pb.go` and `gen/gateway_egress*.pb.go` from the vendored files.
- Add a repeatable contract-drift check that compares semantic descriptors (not comments or the local
  `go_package` option) with `../gutp-building-os-ri/proto/` when the sibling repository is available.
- Run Buf lint and explicitly document the wire-compatible/source-breaking classification of moving
  field 3 into the `oneof`; never renumber fields 1-5 or reuse removed numbers.

### 2. Introduce one typed value through the internal pipeline

- Represent a Common Event's primary `value` as exactly one JSON scalar: number, string, or boolean.
  Existing numeric JSON (`"value": 23.4`) remains valid; no parallel `value_text` attribute convention
  is introduced.
- Use an explicit discriminated Go value type with validation, accessors, and JSON marshal/unmarshal;
  reject null, objects, arrays, non-finite numbers, and ambiguous/missing primary values as poison.
- Keep `attributes` ancillary only. It must not shadow or duplicate the primary reading.

### 3. Map and persist the protobuf oneof without losing presence

- Map number/string/boolean Common Events to `value_num`/`value_str`/`value_bool` in the Normalizer.
- Preserve an explicit numeric `0.0` as a set `value_num` oneof case, rather than an unset value.
- Verify protobuf serialization in the SQLite ring buffer round-trips all three cases, including empty
  string, `false`, and `0.0`, across close/reopen and replay.
- Keep existing ordering, cursor, checkpoint, drift, and drop-oldest behavior unchanged.

### 4. Update connector producers

- MQTT emits JSON number, string, and boolean payloads as the corresponding Common Event scalar.
- BACnet, OPC-UA, Modbus, and simulator adapters preserve native boolean/state values where their
  protocol model exposes them; numeric measurements remain numeric.
- Connector SDK/examples and conformance tests cover the three scalar kinds. Unsupported compound
  payloads are rejected and metered rather than silently coerced.

### 5. Update gateway consumers and operations

- Recent telemetry storage and Admin API JSON preserve the scalar type in `value`.
- Logs and metrics identify the value kind without putting raw string values into metric labels.
- Implement the optional Egress `GatewayStatus` heartbeat using the currently applied Point List ETag;
  older Building OS peers may ignore the additive message.
- Replace stale numeric-only statements in architecture memory, context, connector documentation, and
  EP-003 with the Building OS #152 / ADR-0006 contract decision.

### 6. Verify compatibility end to end

- Unit-test typed JSON validation, Normalizer mapping, protobuf presence, recent-value JSON, and
  Store-and-Forward restart/replay for all three cases.
- Add a mock-gRPC compatibility test that decodes a legacy field-3 numeric frame with the new schema.
- Run an integration test against the real `gutp-building-os-ri` Ingress for number, string, and boolean,
  then assert their types through the Building OS hot telemetry API/persistence path.
- Run Buf lint/code generation checks and the full Go unit/integration suite in CI; keep the real
  cross-repository SoS test manual/nightly where its services are unavailable in per-PR CI.

## Acceptance Criteria

- [ ] Vendored Ingress and Egress descriptors match the current Building OS `gatewaybridge` descriptors,
      excluding only language-specific generation options.
- [ ] Generated Go `TelemetryFrame` exposes the `value_num`, `value_str`, and `value_bool` oneof cases;
      production code no longer references the removed scalar `TelemetryFrame.Value` field.
- [ ] A Common Event contains exactly one primary scalar value of type number, string, or boolean, and
      existing numeric connector payloads remain valid without configuration changes.
- [ ] Normalizer mapping is exhaustive and preserves `0.0`, empty string, and `false` as explicitly set
      oneof cases.
- [ ] SQLite Store-and-Forward preserves the selected oneof case and value across write, process restart,
      reconnect, and replay.
- [ ] MQTT and every shipped connector/simulator either emits native number/string/boolean values or has
      a documented, tested protocol limitation; primary state values are not sent via attributes.
- [ ] Recent telemetry/Admin API output preserves JSON scalar types and remains readable by the Admin UI.
- [ ] Egress sends `GatewayStatus.applied_revision` after Hello and periodically without changing command
      delivery semantics or breaking peers that ignore the additive message.
- [ ] A legacy numeric frame on field 3 is accepted as `value_num`, and a new numeric gateway remains
      interoperable with the current Building OS contract.
- [ ] Real Building OS E2E proves numeric, string, and boolean values retain their type from connector to
      the hot telemetry API/persistence layer.
- [ ] Buf lint, contract-drift checks, Go unit tests, integration tests, and applicable SoS tests pass.
- [ ] Numeric-only design statements are removed or marked superseded in all maintained project docs.

## Child Features

- [x] **FEAT-050: Contract vendor sync and generated code** — synchronize Ingress/Egress proto, regenerate
      Go code, and add semantic contract-drift/Buf checks.
- [x] **FEAT-051: Typed Common Event and Normalizer mapping** — discriminated JSON scalar, validation,
      protobuf oneof mapping, and explicit zero/empty/false presence tests.
- [x] **FEAT-052: Typed Store-and-Forward and uplink compatibility** — persistence/replay tests for every
      case plus legacy field-3 wire-compatibility coverage.
- [x] **FEAT-053: Connector typed-value conformance** — MQTT first, then BACnet/OPC-UA/Modbus/simulator and
      shared SDK fixtures; remove primary-value-in-attributes behavior.
- [x] **FEAT-054: Typed recent telemetry and Admin UI** — preserve and render JSON scalar types safely.
- [x] **FEAT-055: Egress GatewayStatus alignment** — report the applied Point List revision over the
      additive status heartbeat.
- [~] **FEAT-056: Building OS typed-telemetry SoS test and documentation convergence** — exercise all three
      values against the real stack and update superseded architecture/backlog statements.

## Dependencies

- **Authoritative contract:** `../gutp-building-os-ri/proto/gateway_ingress.proto` and
  `gateway_egress.proto`; Building OS #152 Phase A / ADR-0006 is already implemented.
- EP-002 Protocol Connectors — FEAT-053 changes the connector-to-Core Common Event contract.
- EP-003 Normalizer/Uplink — FEAT-051 and FEAT-052 deepen its existing pipeline without changing delivery
  guarantees from ADR-0002.
- EP-004 Admin UI — FEAT-054 changes the live/recent value representation.
- EP-006 Point List Sync — FEAT-055 reports the revision already applied by this module.
- EP-010 SoS E2E — FEAT-056 extends its Ingress assertions to all telemetry scalar types.

## Out of Scope

- Changing the Egress `ControlCommand.present_value` type; Building OS ADR-0006 explicitly limits the
  typed-value change to Ingress Phase A.
- Adding arrays, objects, null, binary values, or new telemetry scalar kinds.
- Changing Store-and-Forward delivery guarantees, stream checkpoint timing, or ack semantics.
- Adding device/metadata registry or command authority to the gateway.

## Delivery Order

`FEAT-050 -> FEAT-051 -> FEAT-052 -> FEAT-053 -> FEAT-054/FEAT-055 -> FEAT-056`

FEAT-054 and FEAT-055 can proceed in parallel after the generated contract and typed pipeline are stable.
The production cutover gate is FEAT-056: do not claim full contract compliance until the real Building OS
round-trip passes for all three value cases.
