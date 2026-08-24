#!/usr/bin/env python3
"""Convert an SBCO standard point-list CSV (the format internal/pointlist/csv.go
parses for --provisioning-file / PROVISIONING_FILE) into the JSON array shape
the MQTT connector needs for MQTT_POINTS_FILE.

The two point lists are NOT interchangeable: the gateway's Point List CSV
resolves local_id -> point_id for normalization, while MQTT_POINTS_FILE tells
the MQTT connector which topics to subscribe to and how to decorate their
events (device_ref, unit, writable). The connector acks-and-drops any message
whose topic is not in MQTT_POINTS_FILE, even under a wildcard subscription
(connector/mqtt/connector.go) — so every topic that must flow through has to
be listed there explicitly, in this different schema.

Usage:
    scripts/csv-to-mqtt-points.py POINT_LIST.csv MQTT_POINTS.json

Only rows whose local_id contains "/" are kept (the same MQTT-topic shape
heuristic InferProtocol uses in internal/pointlist/csv.go), and duplicate
topics are collapsed, first row wins.
"""
import argparse
import csv
import json
import sys


def convert(csv_path, json_path):
    with open(csv_path, newline="", encoding="utf-8-sig") as f:
        reader = csv.DictReader(f)
        seen = set()
        points = []
        for row in reader:
            topic = (row.get("local_id") or "").strip()
            if not topic or "/" not in topic:
                continue
            if topic in seen:
                continue
            seen.add(topic)
            points.append({
                "topic": topic,
                "device_ref": (row.get("device_id") or "").strip(),
                "unit": (row.get("unit") or "").strip(),
                "writable": (row.get("writable") or "").strip().lower() == "true",
            })

    with open(json_path, "w", encoding="utf-8") as f:
        json.dump(points, f, ensure_ascii=False, indent=2)
        f.write("\n")

    print(f"{len(points)} MQTT points written to {json_path}", file=sys.stderr)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("csv_path", help="SBCO point-list CSV (e.g. secrets/THX_StandardPointList_v1.confirmed.csv)")
    parser.add_argument("json_path", help="Output path for the MQTT_POINTS_FILE JSON (e.g. fixtures/mqtt/aws_iot_points.json)")
    args = parser.parse_args()
    convert(args.csv_path, args.json_path)


if __name__ == "__main__":
    main()
