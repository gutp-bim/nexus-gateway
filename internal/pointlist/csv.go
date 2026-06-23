// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointlist

import (
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// LoadCSV parses the SBCO point-list CSV into Entries.
//
// Native address resolution (in priority order):
//  1. object_type_bacnet + instance_no_bacnet both non-empty → "type,instance" (BACnet, backward-compat)
//  2. local_id column non-empty → used as-is (OPC-UA, MQTT, or any protocol)
//
// connector_id: per-row "connector_id" column overrides the connectorID parameter.
// protocol:     inferred from which resolution path was taken ("bacnet" or "opcua");
//               a per-row "protocol" column overrides the inferred value.
//
// Rows with neither a valid BACnet address nor a local_id are skipped.
// Rows with an empty point_id are skipped.
// Duplicate point_id rows are deduplicated (first row wins).
//
// Columns are resolved by header name so column order does not matter.
// A UTF-8 BOM on the first header cell (common in Excel/SBCO exports) is stripped.
// point_id is the only required column; all others are optional.
func LoadCSV(r io.Reader, connectorID string) ([]Entry, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // tolerate ragged rows
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("pointlist: read CSV: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("pointlist: empty CSV")
	}

	// Strip a UTF-8 BOM from the first header cell (common in Excel/SBCO exports).
	rows[0][0] = strings.TrimPrefix(rows[0][0], "\ufeff")
	col := map[string]int{}
	for i, name := range rows[0] {
		col[strings.TrimSpace(name)] = i
	}
	if _, ok := col["point_id"]; !ok {
		return nil, fmt.Errorf("pointlist: CSV missing required column %q", "point_id")
	}

	get := func(row []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	var entries []Entry
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		pointID := get(row, "point_id")
		if pointID == "" {
			continue
		}

		// Resolve native address and protocol.
		objType := get(row, "object_type_bacnet")
		instance := get(row, "instance_no_bacnet")
		localIDCol := get(row, "local_id")

		var localID, proto string
		switch {
		case objType != "" && instance != "":
			// BACnet columns present — construct native address (backward-compat).
			localID = objType + "," + instance
			proto = "bacnet"
		case localIDCol != "":
			// local_id column present — use as-is (OPC-UA, MQTT, …).
			localID = localIDCol
			proto = "opcua"
		default:
			continue // no resolvable native address
		}

		// Per-row protocol column overrides the inferred value.
		if p := get(row, "protocol"); p != "" {
			proto = p
		}

		// Per-row connector_id column overrides the parameter.
		cid := get(row, "connector_id")
		if cid == "" {
			cid = connectorID
		}

		if seen[pointID] {
			slog.Warn("pointlist: duplicate point_id in CSV — ignoring later row", "point_id", pointID)
			continue
		}
		seen[pointID] = true

		entries = append(entries, Entry{
			ConnectorID: cid,
			Protocol:    proto,
			LocalID:     localID,
			PointID:     pointID,
			Unit:        get(row, "unit"),
			Writable:    strings.EqualFold(get(row, "writable"), "true"),
			DeviceRef:   get(row, "device_id"),
		})
	}
	return entries, nil
}
