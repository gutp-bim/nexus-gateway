# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""
OPC UA Sim Gateway → Nexus Gateway integration verification.

Reads values from opcua-sim-gateway (opc.tcp://localhost:4840) and verifies
they match the expected CommonEvent schema for nexus-gateway's normalizer.

Usage:
  # With just opcua-sim-gateway running (no NATS):
  cd ../opcua-sim-gateway && uv run python ../nexus-gateway/integration/opcua_simgw_verify.py

  # With NATS running (full flow):
  NATS_URL=nats://localhost:14222 uv run python ../nexus-gateway/integration/opcua_simgw_verify.py

  # Docker full stack:
  docker compose -f docker-compose.yml -f docker-compose.opcua-simgw.yml up
"""

from __future__ import annotations

import asyncio
import json
import os
import time
from dataclasses import asdict, dataclass
from typing import Any

from asyncua import Client, ua


OPCUA_ENDPOINT = os.environ.get("OPCUA_ENDPOINT", "opc.tcp://localhost:4840")
NATS_URL = os.environ.get("NATS_URL", "")
CONNECTOR_ID = "opcua-01"
PROTOCOL = "opcua"
DEVICE_REF = "DEV001"

# NodeID → (canonical point_id, unit) mapping (mirrors fixtures/point_list_opcua_simgw.json)
POINT_MAP: dict[str, tuple[str, str]] = {
    "ns=2;s=PT001": ("AHU01/supply_air_temp", "degC"),
    "ns=2;s=PT002": ("AHU01/return_humidity", "%RH"),
    "ns=2;s=PT003": ("AHU01/occupancy", ""),
    "ns=2;s=PT004": ("AHU01/damper_cmd", ""),
    "ns=2;s=PT005": ("AHU01/fan_speed", ""),
    "ns=2;s=PT006": ("AHU01/temp_setpoint", "degC"),
    "ns=2;s=PT007": ("AHU01/operation_mode", ""),
    "ns=2;s=PT008": ("AHU01/co2_level", "ppm"),
}


@dataclass
class CommonEvent:
    """CommonEvent wire format (nexus-gateway internal NATS schema)."""
    connector_id: str
    protocol: str
    local_id: str
    device_ref: str
    value: float
    unit: str
    quality: str
    timestamp: str


def _to_float(v: Any) -> float:
    """Coerce OPC UA value to float (bool→0/1, numeric passthrough)."""
    if isinstance(v, bool):
        return 1.0 if v else 0.0
    return float(v)


async def read_all_nodes(client: Client) -> dict[str, Any]:
    """Read current value for every node in POINT_MAP."""
    values: dict[str, Any] = {}
    for node_id in POINT_MAP:
        node = client.get_node(node_id)
        dv = await node.read_data_value()
        values[node_id] = dv.Value.Value
    return values


async def verify_write(client: Client, node_id: str, value: Any) -> bool:
    """Write value to a writable node and read back to confirm."""
    node = client.get_node(node_id)
    dv = await node.read_data_value()
    original = dv.Value.Value
    # Preserve the OPC UA VariantType from the existing value so Int32 stays Int32 etc.
    orig_variant_type = dv.Value.VariantType
    try:
        await node.write_value(ua.DataValue(ua.Variant(value, orig_variant_type)))
        after = (await node.read_data_value()).Value.Value
        ok = after == value
        # Restore original
        await node.write_value(ua.DataValue(ua.Variant(original, orig_variant_type)))
        return ok
    except ua.UaStatusCodeError as e:
        print(f"  WARN: write to {node_id} rejected: {e}")
        return False


async def main() -> None:
    print(f"Connecting to {OPCUA_ENDPOINT} ...")
    async with Client(OPCUA_ENDPOINT) as client:
        print("Connected.\n")

        # ── Read all nodes ────────────────────────────────────────────────────
        print("Reading all point values:")
        values = await read_all_nodes(client)
        events: list[CommonEvent] = []
        for node_id, raw_value in values.items():
            _, unit = POINT_MAP[node_id]
            value = _to_float(raw_value)
            evt = CommonEvent(
                connector_id=CONNECTOR_ID,
                protocol=PROTOCOL,
                local_id=node_id,
                device_ref=DEVICE_REF,
                value=value,
                unit=unit,
                quality="Good",
                timestamp=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            )
            events.append(evt)
            print(f"  {node_id:<18} {raw_value!r:>12}  → CommonEvent(point={POINT_MAP[node_id][0]}, value={value})")

        # ── Write-round-trip for writable nodes ───────────────────────────────
        print("\nWrite round-trip test (writable nodes):")
        writable_nodes = {
            "ns=2;s=PT004": False,   # DamperCommand (Boolean)
            "ns=2;s=PT006": 25.0,   # TemperatureSetpoint (Double)
            "ns=2;s=PT007": 0,      # OperationMode (Int32)
        }
        all_ok = True
        for node_id, test_value in writable_nodes.items():
            ok = await verify_write(client, node_id, test_value)
            status = "✓ PASS" if ok else "✗ FAIL"
            print(f"  {node_id:<18} write({test_value!r}) read-back → {status}")
            if not ok:
                all_ok = False

        # ── Publish to NATS if available ──────────────────────────────────────
        if NATS_URL:
            import nats as nats_py
            print(f"\nPublishing {len(events)} events to NATS ({NATS_URL}) ...")
            nc = await nats_py.connect(NATS_URL)
            for evt in events:
                subject = f"evt.{evt.protocol}.{evt.connector_id}"
                await nc.publish(subject, json.dumps(asdict(evt)).encode())
                print(f"  Published → {subject}")
            await nc.drain()
            print("Done. Check nexus-gateway normalizer output.")
        else:
            print("\n(NATS_URL not set — skipping publish. Set NATS_URL=nats://... to publish events.)")

        print("\n── Summary ──────────────────────────────────────────────────────────")
        print(f"  OPC UA nodes read : {len(values)}/{len(POINT_MAP)}")
        print(f"  Write round-trip  : {'PASS' if all_ok else 'FAIL'}")
        print(f"  NATS publish      : {'yes (' + NATS_URL + ')' if NATS_URL else 'skipped'}")


if __name__ == "__main__":
    asyncio.run(main())
