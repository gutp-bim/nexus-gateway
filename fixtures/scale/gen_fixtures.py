#!/usr/bin/env python3
# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Scale fixture generator for BOS resource management verification.

Verification scenario
---------------------
The simulator (bacnet-sim-gateway, opcua-sim-gateway) runs with the FULL
set of points at all times.  Connectors are configured with the full set so
they can receive any data the simulator publishes.

The BOS (Building OS) is the system under test.  Point lists of increasing
size are imported into the BOS, which provisions the gateway accordingly.
The gateway normalises Common Events using what the BOS says — not a local
file.

    Simulator (full 2000 pts always on)
        ↕  BACnet/OPC-UA
    Connectors (full config — fixtures/scale/connectors/<id>.json)
        ↕  NATS
    Gateway  ←→  BOS  ← import fixtures/scale/<N>/point_list.csv
                          to test resource management at scale N

File layout
-----------
  fixtures/scale/
    connectors/             Full connector configs (BACNET_POINTS / OPCUA_POINTS).
      bacnet-01.json        One file per connector, always the full point set.
      bacnet-02.json        Derived from the 2000-pt master; never edited directly.
      ...
      opcua-01.json
    2000/                   Master / source of truth
      point_list.json
      point_list.csv
    1000/                   BOS import fixture — 1000-pt verification
      point_list.json
      point_list.csv
    500/                    BOS import fixture — 500-pt verification
    200/                    BOS import fixture — 200-pt verification
    gen_fixtures.py         This script

Protocol distribution (master list is interleaved 1:1)
-------------------------------------------------------
   200 pts  → BACnet 100  + OPC-UA 100
   500 pts  → BACnet 250  + OPC-UA 250
  1000 pts  → BACnet 500  + OPC-UA 500
  2000 pts  → BACnet 1000 + OPC-UA 1000

Usage
-----
  # Regenerate all scale tiers and connector files
  python3 fixtures/scale/gen_fixtures.py

  # Custom sizes
  python3 fixtures/scale/gen_fixtures.py --sizes 100 300

  # Preview distribution without writing files
  python3 fixtures/scale/gen_fixtures.py --dry-run

  # CSV only (e.g. after point metadata update)
  python3 fixtures/scale/gen_fixtures.py --csv-only
"""
from __future__ import annotations

import argparse
import csv
import json
import sys
from collections import Counter, defaultdict
from pathlib import Path

MASTER = Path(__file__).parent / "2000/point_list.json"
OUT_ROOT = Path(__file__).parent
DEFAULT_SIZES = [200, 500, 1000, 2000]

CSV_FIELDS = [
    "gateway_id", "connector_id", "protocol",
    "point_id", "point_name", "point_type", "point_specification",
    "writable", "interval", "unit",
    "max_pres_value", "min_pres_value", "labels", "scale",
    "device_id", "device_name", "device_type",
    "site", "building", "floor", "installation_area",
    "target_area", "panel", "tags", "supplier", "owner", "description",
    "local_id", "device_id_bacnet", "instance_no_bacnet", "object_type_bacnet",
]

# unit → point metadata  (point_type must match _POINT_SEED in bacnet-sim-gateway/semantic/brick.py)
_UNIT_META: dict[str, dict] = {
    "degC": {"point_type": "Temperature",          "point_name": "Temperature",          "point_specification": "Measurement", "max_pres_value": "50",     "min_pres_value": "-10"},
    "%RH":  {"point_type": "Humidity",             "point_name": "Humidity",             "point_specification": "Measurement", "max_pres_value": "100",    "min_pres_value": "0"},
    "ppm":  {"point_type": "CO2 Concentration",    "point_name": "CO2 Concentration",    "point_specification": "Measurement", "max_pres_value": "5000",   "min_pres_value": "0"},
    "Pa":   {"point_type": "Differential Pressure","point_name": "Differential Pressure","point_specification": "Measurement", "max_pres_value": "1000",   "min_pres_value": "-1000"},
    "m3/h": {"point_type": "Flow Rate",            "point_name": "Flow Rate",            "point_specification": "Measurement", "max_pres_value": "10000",  "min_pres_value": "0"},
    "kW":   {"point_type": "Power",                "point_name": "Power",                "point_specification": "Measurement", "max_pres_value": "1000",   "min_pres_value": "0"},
    "kWh":  {"point_type": "Energy",               "point_name": "Energy",               "point_specification": "Metering",    "max_pres_value": "999999", "min_pres_value": "0"},
    "lux":  {"point_type": "Illuminance",          "point_name": "Illuminance",          "point_specification": "Measurement", "max_pres_value": "10000",  "min_pres_value": "0"},
    "m/s":  {"point_type": "Wind Speed",           "point_name": "Wind Speed",           "point_specification": "Measurement", "max_pres_value": "50",     "min_pres_value": "0"},
    "bar":  {"point_type": "Pressure",             "point_name": "Pressure",             "point_specification": "Measurement", "max_pres_value": "10",     "min_pres_value": "0"},
}

# device_ref → device metadata
_DEVICE_META: dict[str, dict] = {
    "ahu-01":       {"device_id": "ahu-01",       "device_name": "AHU-01",       "device_type": "AHU",     "device_id_bacnet": "1001"},
    "ahu-02":       {"device_id": "ahu-02",       "device_name": "AHU-02",       "device_type": "AHU",     "device_id_bacnet": "1002"},
    "fcu-01":       {"device_id": "fcu-01",       "device_name": "FCU-01",       "device_type": "AHU",     "device_id_bacnet": "1003"},
    "chiller-01":   {"device_id": "chiller-01",   "device_name": "Chiller-01",   "device_type": "Chiller", "device_id_bacnet": "1004"},
    "env-01":       {"device_id": "env-01",       "device_name": "ENV-01",       "device_type": "Sensor",  "device_id_bacnet": "1005"},
    "opcua-server": {"device_id": "opcua-server", "device_name": "OPCUA Server", "device_type": "Sensor",  "device_id_bacnet": ""},
}


def _derive_point_meta(p: dict) -> dict:
    """Return point_type/name/specification/max/min/labels for a point."""
    unit = p.get("unit", "")
    protocol = p.get("protocol", "")
    local_id = p.get("local_id", "")
    point_id = p.get("point_id", "")
    writable = p.get("writable", False)

    # BACnet binary points: specification from object type regardless of unit
    if protocol == "bacnet" and "," in local_id:
        obj_type = local_id.split(",")[0]
        if obj_type == "binaryInput":
            return {"point_type": "Status", "point_name": "Operation Status", "point_specification": "Status",
                    "max_pres_value": "", "min_pres_value": "", "labels": "Off&&On"}
        if obj_type == "binaryOutput":
            return {"point_type": "HVAC Control", "point_name": "Start/Stop Command", "point_specification": "Command",
                    "max_pres_value": "", "min_pres_value": "", "labels": "Off&&On"}
        # analogOutput: keep unit-based type/range but set Setpoint
        if obj_type == "analogOutput":
            base = _UNIT_META.get(unit, {
                "point_type": "Setpoint", "point_name": "Setpoint",
                "max_pres_value": "100", "min_pres_value": "0",
            })
            return {**base, "point_specification": "Setpoint", "labels": ""}

    # OPC-UA binary (UA-B-*): boolean on/off points
    if protocol == "opcua" and point_id.startswith("UA-B-"):
        if writable:
            return {"point_type": "HVAC Control", "point_name": "Start/Stop Command", "point_specification": "Command",
                    "max_pres_value": "", "min_pres_value": "", "labels": "Off&&On"}
        return {"point_type": "Status", "point_name": "Operation Status", "point_specification": "Status",
                "max_pres_value": "", "min_pres_value": "", "labels": "Off&&On"}

    # OPC-UA integer (UA-I-*): integer setpoints / mode commands
    if protocol == "opcua" and point_id.startswith("UA-I-"):
        return {"point_type": "Setpoint", "point_name": "Setpoint",
                "point_specification": "Setpoint", "max_pres_value": "", "min_pres_value": "", "labels": ""}

    # analogInput and OPC-UA UA-F-*: derive from unit
    if unit in _UNIT_META:
        return dict(_UNIT_META[unit], labels="")

    # Fallback
    return {"point_type": unit or "Unknown", "point_name": unit or "Unknown",
            "point_specification": "Measurement", "max_pres_value": "", "min_pres_value": "", "labels": ""}


def enrich_point(p: dict) -> dict:
    """Return a CSV-ready row with all spec fields derived from master fields."""
    unit = p.get("unit", "")
    device_ref = p.get("device_ref", "")
    local_id = p.get("local_id", "")
    protocol = p.get("protocol", "")

    pt_meta = _derive_point_meta(p)
    dev_meta = _DEVICE_META.get(device_ref, {
        "device_id": device_ref, "device_name": device_ref,
        "device_type": "", "device_id_bacnet": "",
    })

    # BACnet: local_id = "objectType,instanceNo"
    if protocol == "bacnet" and "," in local_id:
        obj_type, inst_no = local_id.split(",", 1)
    else:
        obj_type, inst_no = "", ""

    return {
        "gateway_id":          "gw-001",
        "connector_id":        p.get("connector_id", ""),
        "protocol":            protocol,
        "point_id":            p.get("point_id", ""),
        "point_name":          pt_meta["point_name"],
        "point_type":          pt_meta["point_type"],
        "point_specification": pt_meta["point_specification"],
        "writable":            str(p.get("writable", False)).lower(),
        "interval":            "5",
        "unit":                unit,
        "max_pres_value":      pt_meta["max_pres_value"],
        "min_pres_value":      pt_meta["min_pres_value"],
        "labels":              pt_meta["labels"],
        "scale":               "1.0",
        "device_id":           dev_meta["device_id"],
        "device_name":         dev_meta["device_name"],
        "device_type":         dev_meta["device_type"],
        "site":                "Site01",
        "building":            "Building01",
        "floor":               "1F",
        "installation_area":   "Area01",
        "target_area":         "",
        "panel":               "",
        "tags":                "",
        "supplier":            "",
        "owner":               "",
        "description":         "",
        "local_id":            local_id,
        "device_id_bacnet":    dev_meta["device_id_bacnet"],
        "instance_no_bacnet":  inst_no,
        "object_type_bacnet":  obj_type,
    }


def load_master(path: Path) -> list[dict]:
    if not path.exists():
        print(f"ERROR: master list not found: {path}", file=sys.stderr)
        sys.exit(1)
    return json.loads(path.read_text())


def write_json(path: Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2))


def write_csv(path: Path, points: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=CSV_FIELDS)
        writer.writeheader()
        for p in points:
            writer.writerow(enrich_point(p))


def derive_connector_points(points: list[dict]) -> dict[str, list[dict]]:
    """Strip gateway-side fields, group by connector_id."""
    by_connector: dict[str, list[dict]] = defaultdict(list)
    for p in points:
        by_connector[p["connector_id"]].append({
            "local_id": p["local_id"],
            "device_ref": p.get("device_ref", ""),
            "unit": p.get("unit", ""),
            "writable": bool(p.get("writable", False)),
        })
    return by_connector


def generate_bos_import(size: int, master: list[dict],
                        *, csv_only: bool, dry_run: bool) -> None:
    """Generate BOS import fixtures for a given point count."""
    if size > len(master):
        print(f"  WARN: {size} > master ({len(master)}), capped")
        size = len(master)

    points = master[:size]
    proto = Counter(p["protocol"] for p in points)
    cids  = Counter(p["connector_id"] for p in points)
    print(f"  {size:>5} pts  "
          f"BACnet={proto.get('bacnet',0):>4}  OPC-UA={proto.get('opcua',0):>4}  "
          f"connectors: {dict(sorted(cids.items()))}")

    if dry_run:
        return

    out = OUT_ROOT / str(size)
    if not csv_only:
        write_json(out / "point_list.json", points)
    write_csv(out / "point_list.csv", points)
    for proto in ("bacnet", "opcua"):
        proto_points = [p for p in points if p["protocol"] == proto]
        if proto_points:
            write_csv(out / f"point_list_{proto}.csv", proto_points)


def generate_connector_configs(master: list[dict], dry_run: bool) -> None:
    """Derive full connector configs from the master list."""
    by_connector = derive_connector_points(master)
    out_dir = OUT_ROOT / "connectors"
    print(f"\nConnector configs → {out_dir}/")
    for cid, pts in sorted(by_connector.items()):
        print(f"  {cid}.json  ({len(pts)} pts)")
        if not dry_run:
            write_json(out_dir / f"{cid}.json", pts)


def main() -> None:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument("--sizes", type=int, nargs="+", default=DEFAULT_SIZES,
                        help=f"BOS import sizes to generate (default: {DEFAULT_SIZES})")
    parser.add_argument("--master", type=Path, default=MASTER,
                        help=f"master point list (default: {MASTER})")
    parser.add_argument("--csv-only", action="store_true",
                        help="only regenerate CSV files, skip JSON")
    parser.add_argument("--dry-run", action="store_true",
                        help="show what would be generated without writing")
    parser.add_argument("--no-connectors", action="store_true",
                        help="skip regenerating connector config files")
    args = parser.parse_args()

    master = load_master(args.master)
    proto_total = Counter(p["protocol"] for p in master)
    print(f"Master: {args.master}  total={len(master)}  {dict(proto_total)}")
    print(f"\nBOS import fixtures ({['generating','dry-run'][args.dry_run]}):")

    for size in sorted(args.sizes):
        generate_bos_import(size, master, csv_only=args.csv_only, dry_run=args.dry_run)

    if not args.no_connectors and not args.csv_only:
        generate_connector_configs(master, args.dry_run)

    if not args.dry_run:
        print("\n--- Usage ---")
        print("BOS import:  fixtures/scale/<N>/point_list.csv  (import into BOS)")
        print("Connectors:  BACNET_POINTS=$(cat fixtures/scale/connectors/bacnet-01.json)")
        print("             OPCUA_POINTS=$(cat fixtures/scale/connectors/opcua-01.json)")


if __name__ == "__main__":
    main()
