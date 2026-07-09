---
name: architecture
description: System architecture overview for nexus-gateway
metadata:
  type: project
---

# Architecture

> Status: target architecture / design intent. Some elements below are
> forward-looking (e.g. Modbus connector, OpenTelemetry Collector); for what
> currently ships see [README](../../README.md) and [CONTEXT.md](../../CONTEXT.md).

## System Diagram

```
                 Building OS
                       ▲
                       │  gRPC (only contract)
                       │
┌────────────────────────────────────┐
│ Integration Gateway                │
│                                    │
│  Admin UI                          │
│      │                             │
│      ▼                             │
│  Core Agent (Go)                   │
│      ├── Connector Registry        │
│      ├── Lifecycle Manager         │
│      ├── Config Manager            │
│      ├── Health Monitor            │
│      └── Admin API                 │
│                                    │
│  Normalizer                        │
│      ▼                             │
│  NATS JetStream                    │
│                                    │
│  Connector Containers              │
│      ├── BACnet (Python)           │
│      ├── OPC-UA (Java)             │
│      ├── MQTT (Go/Python)          │
│      ├── Modbus (planned)          │
│      └── Future Extensions         │
│                                    │
│  SQLite Buffer (store & forward)   │
│  OpenTelemetry Collector           │
└────────────────────────────────────┘
```

## Components

| Component | Responsibility |
|-----------|---------------|
| Core Agent (Go) | Orchestrates connector lifecycle, config, health, and exposes the Admin API. Manages containers via Docker Engine SDK. |
| Connector Registry | Tracks available/installed connectors and their versions. |
| Lifecycle Manager | Start/Stop/Restart/Upgrade connector containers. |
| Config Manager | Distributes and persists connector + gateway configuration. |
| Health Monitor | Tracks gateway and connector health (uptime, CPU, memory, disk). |
| Admin API | Backend for the Admin UI; OIDC/OAuth2 protected. |
| Connectors | Per-protocol independent containers. Emit common event format only; no equipment-specific models; no inter-connector dependencies. |
| Normalizer | Strips protocol-dependent info; unifies Point ID, Device ID, timestamp, quality, unit. Hosts future semantic mapping (REC/Brick/QUDT/BOT). |
| NATS JetStream | Internal pub/sub transport for connector events; replay and short-term retention. |
| SQLite Buffer | Local store-and-forward during uplink outages. |
| gRPC Uplink | Building OS-owned contract (`gatewaybridge`, vendored from `../gutp-building-os-oss/proto/`): Ingress `GatewayIngress/StreamTelemetry` + Egress `GatewayEgress/Connect`. No DiscoveryService. |
| OpenTelemetry Collector | Collects metrics/logs/traces; exports OTLP + Prometheus. |
| Admin UI | React/Next.js operator console. |
| Connector Catalog (external) | Standalone management server serving approved connector manifests (image digest, permissions, signature policy). Polled by the Core Agent; see ADR-0006. |

## Technology Stack

- **Core Agent:** Go — grpc-go, Docker Engine SDK, nats.go, OpenTelemetry SDK.
- **API definition:** Protocol Buffers, managed with Buf (schema mgmt, codegen, breaking-change detection).
- **Messaging:** NATS JetStream.
- **Local buffer:** SQLite.
- **Connectors:** BACnet (Python — BACpypes3, BAC0), OPC-UA (Java — Eclipse Milo), MQTT (Go, MQTT 5.0). Modbus (Go, Modbus TCP) is **planned, not yet implemented**.
- **Admin UI:** React, Next.js, shadcn/ui, TanStack Table.
- **Auth:** Keycloak (OIDC/OAuth2) for Admin UI, Admin API, Building OS connection.
- **Observability:** OpenTelemetry → OTLP, Prometheus.

## Common Event Model (connector output)

Native addressing only — no canonical `point_id`/`device_id` (ADR-0001). Published to JetStream `EVENTS` on `evt.<protocol>.<connector_id>` (ADR-0005).

```json
{
  "protocol": "opcua",
  "connector_id": "opcua-01",
  "local_id": "ns=2;s=AHU01.SupplyAirTemp",
  "device_ref": "opc.tcp://192.0.2.10:4840",
  "value": 23.4,
  "unit": "Cel",
  "quality": "Good",
  "timestamp": "2026-01-01T00:00:00Z"
}
```

## Open Questions

- [x] Connector ↔ Core transport → ADR-0005 (single `EVENTS` stream, `evt.<protocol>.<connector_id>`, limits 48 h / 2 GB DiscardOld).
- [x] SQLite buffer / replay cursor semantics → ADR-0002 + decisions.md (ring buffer; immediate send + 5 s/1000-frame ack checkpoint).
- [x] Normalizer ID-mapping source of truth → ADR-0003 (Building OS twin, diff sync, EP-006).
- [x] Telemetry batching/acking granularity → decisions.md (ack only on stream close ⇒ checkpoint = stream rotation).
- [x] Connector upgrade flow → ADR-0006 / EP-007 (catalog-mediated signed OCI, digest-pinned, stop-replace-rollback; connector OTA brought into scope 2026-06-13).
