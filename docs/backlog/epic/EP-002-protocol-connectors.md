# EP-002: Protocol Connectors

**Status:** Prod
**Priority:** P0

## Goal

Connectors are independent per-protocol containers that talk to field equipment and publish **Common Events** onto NATS JetStream. They absorb protocol diversity at the edge and hold **no canonical identity and no domain model** (ADR-0001): identity resolution is the Normalizer's job. MVP-1 requires BACnet, OPC-UA, and MQTT; Modbus and future protocols follow the same container pattern.

## Acceptance Criteria

- [ ] Each connector is an isolated container with no dependency on other connectors and no Building-OS-specific or equipment-specific domain model.
- [ ] Every connector publishes Common Events carrying `protocol` and **native addressing only** (`local_id` + native device ref) plus raw value/unit/quality/timestamp — **no canonical `point_id`/`device_id`** (ADR-0001).
- [ ] Common Events are published to JetStream stream `EVENTS` on subject `evt.<protocol>.<connector_id>`; `local_id` rides in the payload, never the subject (ADR-0005).
- [ ] Connectors publish async with a bounded in-flight ack window and never block on a full stream (ADR-0005).
- [ ] Connectors read the Point List only for the native addresses to poll/subscribe; canonical columns are ignored (ADR-0001).
- [ ] BACnet connector (Python, BACpypes3/BAC0) supports Who-Is, I-Am, ReadProperty, ReadPropertyMultiple, SubscribeCOV for telemetry. (Write support is EP-005.)
- [ ] OPC-UA connector (Java, Eclipse Milo) supports Browse, Read, Subscribe for telemetry. (Write/Method Call support is EP-005.)
- [ ] MQTT connector (**Go**, paho.golang) supports MQTT 5.0.
- [ ] (Post-MVP) Modbus connector (**Go**) supports Modbus TCP.

## Child Features

- [ ] FEAT-006: Connector Common Event contract + per-language SDK (Go/Python/Java: NATS publish, config load, health reporting)
- [ ] FEAT-007: BACnet connector (telemetry)
- [ ] FEAT-008: OPC-UA connector (telemetry)
- [ ] FEAT-009: MQTT connector (Go)
  <!-- Entrypoint (cmd/mqtt-connector/main.go) and Dockerfile added; run with docker-compose.mqtt.yml.
       Remaining: freshness-floor re-publish (connector-spec §3.4). -->
- [ ] FEAT-010: Modbus connector (Go, post-MVP)

## Dependencies

- EP-006 Point List Sync — connectors derive their poll/subscribe lists from the synced Point List.
- Control-path write handlers inside each connector are tracked in EP-005, not here.
