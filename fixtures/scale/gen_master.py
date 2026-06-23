#!/usr/bin/env python3
# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Generate the scale master point list (2000/point_list.json).

Single-device BACnet
--------------------
The BACnet simulator is ONE physical device (device id 1001). The connector
always reads that single device (BACNET_DEVICE_ID), so every BACnet point must
have a *distinct* native address ``objectType,instance`` — otherwise multiple
logical points collapse onto the same object and only the distinct addresses are
ever ingested.

This generator emits 1000 distinct BACnet objects on the single device, with the
same per-type proportions the test plan uses, and contiguous instances per type
so the addresses match what the simulator registers from the same point list:

    analogInput   1..500   (read-only,  measurement units)
    analogOutput  1..200   (writable,   setpoint units)
    binaryInput   1..200   (read-only,  status)
    binaryOutput  1..100   (writable,   command)

OPC-UA points (1000) are carried over unchanged from the existing master — they
are already distinct (UA-F-/UA-B-/UA-I-).

BACnet : OPC-UA are interleaved 1:1 so each BOS-import tier (master[:N]) keeps a
balanced protocol split. After regenerating the master, run gen_fixtures.py to
slice the tiers and derive connector configs.

Usage:
    python3 fixtures/scale/gen_master.py        # writes 2000/point_list.json
    python3 fixtures/scale/gen_fixtures.py       # then regenerate tiers/connectors
"""
from __future__ import annotations

import json
from pathlib import Path

HERE = Path(__file__).parent
MASTER = HERE / "2000/point_list.json"

# Single BACnet device — device_ref "ahu-01" maps to device_id_bacnet 1001 in
# gen_fixtures._DEVICE_META.
BACNET_CONNECTOR = "bacnet-01"
BACNET_DEVICE_REF = "ahu-01"

# Per-type counts (sum = 1000), preserving the test plan's proportions.
BACNET_LAYOUT = [
    # (objectType, count, writable, unit_cycle, point_id_tag)
    ("analogInput",  500, False, ["degC", "%RH", "ppm", "Pa", "m3/h", "kW", "kWh", "lux", "m/s", "bar"], "AI"),
    ("analogOutput", 200, True,  ["degC", "%RH", "Pa", "m3/h"],                                          "AO"),
    ("binaryInput",  200, False, [""],                                                                   "BI"),
    ("binaryOutput", 100, True,  [""],                                                                   "BO"),
]


def build_bacnet() -> list[dict]:
    points: list[dict] = []
    for obj_type, count, writable, units, tag in BACNET_LAYOUT:
        for inst in range(1, count + 1):
            unit = units[(inst - 1) % len(units)]
            points.append({
                "connector_id": BACNET_CONNECTOR,
                "device_ref": BACNET_DEVICE_REF,
                "protocol": "bacnet",
                "local_id": f"{obj_type},{inst}",
                "point_id": f"BN-{tag}-{inst:04d}",
                "unit": unit,
                "writable": writable,
            })
    return points


def load_opcua(master_path: Path) -> list[dict]:
    if not master_path.exists():
        raise SystemExit(f"existing master not found (need OPC-UA points): {master_path}")
    master = json.loads(master_path.read_text())
    return [p for p in master if p.get("protocol") == "opcua"]


def interleave(a: list[dict], b: list[dict]) -> list[dict]:
    """1:1 interleave; trailing remainder of the longer list is appended."""
    out: list[dict] = []
    for i in range(max(len(a), len(b))):
        if i < len(a):
            out.append(a[i])
        if i < len(b):
            out.append(b[i])
    return out


def main() -> None:
    bacnet = build_bacnet()
    opcua = load_opcua(MASTER)

    # Sanity: BACnet addresses must be globally distinct.
    ids = [p["local_id"] for p in bacnet]
    assert len(ids) == len(set(ids)), "BACnet local_ids must be distinct"

    master = interleave(bacnet, opcua)
    MASTER.write_text(json.dumps(master, ensure_ascii=False, indent=2))
    print(f"wrote {MASTER}  total={len(master)}  "
          f"bacnet={len(bacnet)} (distinct {len(set(ids))})  opcua={len(opcua)}")


if __name__ == "__main__":
    main()
