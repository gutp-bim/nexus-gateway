# EP-005: Control Path

**Status:** Prod
**Priority:** P0

## Goal

Deliver the downlink: Building OS pushes Control Commands to physical equipment through the gateway, reliably **within the live window** and never replayed across an outage (ADR-0004). This is a vertical feature spanning the Core Agent (Egress stream client + dispatch), the Point List resolver (reverse lookup), and each Connector (write handler) — the mirror of the telemetry path with the opposite delivery policy.

## Acceptance Criteria

- [ ] Core Agent dials `gatewaybridge.GatewayEgress/Connect` (bidi), opens with `Hello{gateway_id}`, and reconnects with backoff.
- [ ] On `ControlCommand{control_id, point_id, present_value, priority}`, the Core Agent resolves `point_id` → native `(protocol, device/object/instance)` via the shared Point List resolver (EP-006) and dispatches to the owning Connector over **core NATS request-reply** on `cmd.<protocol>.<connector_id>` — never JetStream, never persisted (ADR-0004, ADR-0005).
- [ ] Dispatch is deadline-bounded and idempotent on `control_id`; commands are never buffered or replayed across an outage.
- [ ] Every command returns exactly one definitive `ControlResult{control_id, success, response}`; failures are typed: `timeout` / `no_connector` / `not_writable` / `device_error`.
- [ ] BACnet connector executes writes via WriteProperty/WritePropertyMultiple honoring BACnet write `priority`.
- [ ] OPC-UA connector executes writes via Write / Method Call.
- [ ] MQTT connector publishes write payloads per point control schema.
  <!-- Write handler implemented in connector/mqtt/connector.go (handleWrite).
       Remaining: end-to-end validation against the Point List control schema (FEAT-024). -->
- [ ] Writeability is validated against the Point List before dispatch (`not_writable` without touching the device).

## Child Features

- [ ] FEAT-021: Egress stream client (Hello, reconnect/backoff, result correlation)
- [ ] FEAT-022: Command Channel dispatch (core NATS request-reply, deadline, idempotency on `control_id`, typed failures)
- [ ] FEAT-023: Connector write handlers (BACnet / OPC-UA / MQTT)
- [ ] FEAT-024: Writeability + control-schema validation against the Point List

## Dependencies

- EP-001 Core Agent — hosts the Egress client and dispatcher.
- EP-002 Protocol Connectors — write handlers extend each connector container.
- EP-006 Point List Sync — the same resolver serves both directions (native↔`point_id`).
