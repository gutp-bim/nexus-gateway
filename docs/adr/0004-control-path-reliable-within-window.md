# Control path: reliable within the live window, never replay a stale command

**Status:** accepted — 2026-06-13

The control (downlink) path is the mirror of telemetry and gets the **opposite** delivery policy. The Core Agent holds the gateway-dialed Egress bidi stream (`gatewaybridge.GatewayEgress/Connect`: `Hello{gateway_id}`, backoff reconnect). It receives `ControlCommand`, resolves `point_id` → native `(protocol, device/object/instance)` via the shared Point List resolver (the reverse of the Normalizer's lookup; same resolver), and dispatches to the owning Connector over **core NATS request-reply — not JetStream**. Commands are **never persisted or buffered**: a stale write applied to physical equipment after an outage is dangerous, so cross-outage replay is forbidden.

"Don't lose a command" is satisfied *within the live execution window*, not by persistence:

- **No-responder → instant typed failure** (`no_connector`) instead of waiting out the full timeout.
- **Bounded retry inside the deadline**, with the Connector de-duplicating on `control_id` (short TTL) so a retry never double-writes.
- **Exactly one definitive `ControlResult` per `control_id`** is always returned up the Egress stream — success, or a typed failure (`timeout` / `no_connector` / `not_writable` / `device_error`). Building OS therefore gets an explicit answer, never just its own `WaitForResult` timeout.
- A ready result is **held briefly** across an Egress blip and sent on reconnect (bounded; Building OS's `WaitForResult` is itself time-limited). The command body is never held.

## Consequences

- A Core Agent crash *mid-command* is not recovered by persistence: Building OS times out and the operator retries with a **new `control_id`**. Accepted, to preserve the no-stale-write invariant.
- `priority` passes through to the Connector (BACnet write priority; other protocols map or ignore).
- `control_id` is the end-to-end correlation key, symmetric with Building OS's `Nats-Msg-Id = controlId` de-dup.
