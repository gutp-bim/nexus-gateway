---
name: pitfalls
description: Known failure modes and gotchas for nexus-gateway
metadata:
  type: project
---

# Pitfalls

> Add entries here when something burns time or causes a subtle bug.

## Leaking protocol-specific data into the common event model

Connectors must emit only the common event format. Do not pass through BACnet object IDs, OPC-UA NodeIds, or other protocol-native identifiers in a way that couples downstream consumers to a protocol. All protocol-dependent info is stripped/unified in the Normalizer, not the connector.

## Breaking the gRPC contract to Building OS

Changing field numbers, removing fields, or renaming services in the Protobuf definitions breaks the Building OS contract even if it compiles. Always run Buf breaking-change detection before merging `.proto` changes.

## Assuming the uplink is always available

Building OS connectivity can drop. Any telemetry path that does not go through the SQLite store-and-forward buffer risks data loss on outage. Verify ordering and replay behavior on reconnect.
