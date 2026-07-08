// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointlist

import (
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

const (
	protocolBACnet = "bacnet"
	protocolOPCUA  = "opcua"
	protocolMQTT   = "mqtt"
)

// protocolPatterns infers a row's protocol from its local_id when no explicit
// "protocol" column is present. Order matters: first match wins. Extending to
// a future protocol (e.g. Modbus) is a one-line addition once that connector
// defines a self-describing local_id convention — see the Modbus note below.
var protocolPatterns = []struct {
	protocol string
	re       *regexp.Regexp
}{
	{protocolOPCUA, regexp.MustCompile(`^ns=\d+;[isgb]=`)}, // OPC-UA NodeId, e.g. "ns=2;s=PT001"
	{protocolMQTT, regexp.MustCompile(`/`)},                // MQTT topic, e.g. "sensors/room1/temp"
	// Modbus: no established local_id convention exists yet in this codebase
	// (no connector implementation, no fixture, no spec). When it is built,
	// give it a self-describing prefix (e.g. "holding:100", "coil:5") and add
	// its pattern here. Do NOT use bare numeric registers — they have no
	// distinguishing token and would be unclassifiable by this inference.
}

// CSVOptions configures LoadCSV's per-row connector_id resolution.
type CSVOptions struct {
	// ConnectorID is the fallback connector_id for a row without an explicit
	// connector_id column whose protocol is not found in ConnectorMap (this
	// covers "bacnet" when ConnectorMap has no "bacnet" entry, "unknown", and
	// any other/future protocol). Sourced from PROVISIONING_CONNECTOR_ID.
	ConnectorID string
	// ConnectorMap resolves protocol -> connector_id for rows without an
	// explicit connector_id column (CONNECTOR_MAP, shared with HTTP
	// provisioning). A protocol not present in the map, or a nil/empty map,
	// falls back to ConnectorID.
	ConnectorMap map[string]string
}

// LoadCSV parses the SBCO point-list CSV into Entries.
//
// Native address / protocol resolution (in priority order):
//  1. object_type_bacnet + instance_no_bacnet both non-empty → local_id "type,instance", protocol "bacnet" (backward-compat)
//  2. local_id column non-empty → used as-is; protocol comes from the per-row
//     "protocol" column (case-insensitive) if present, else is inferred from
//     the local_id's shape via protocolPatterns, else "unknown" (logged).
//
// connector_id: per-row "connector_id" column overrides everything below it.
// Otherwise ConnectorMap[protocol] is used; if the protocol has no entry (or
// ConnectorMap is empty), ConnectorID is used.
//
// Rows with neither a valid BACnet address nor a local_id are skipped.
// Rows with an empty point_id are skipped.
// Duplicate point_id rows are deduplicated (first row wins).
//
// Columns are resolved by header name so column order does not matter.
// A UTF-8 BOM on the first header cell (common in Excel/SBCO exports) is stripped.
// point_id is the only required column; all others are optional.
func LoadCSV(r io.Reader, opts CSVOptions) ([]Entry, error) {
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
			proto = protocolBACnet
		case localIDCol != "":
			// local_id column present — use as-is (OPC-UA, MQTT, …). Protocol is
			// resolved below (explicit column, else pattern inference).
			localID = localIDCol
		default:
			continue // no resolvable native address
		}

		if proto != protocolBACnet {
			// Per-row protocol column overrides inference, normalized so
			// "OPCUA"/"Opcua"/"opcua" are all recognized alike.
			if p := strings.ToLower(strings.TrimSpace(get(row, "protocol"))); p != "" {
				proto = p
			} else {
				proto = inferProtocol(localID)
				if proto == "" {
					proto = "unknown"
					slog.Warn("pointlist: could not infer protocol from local_id; add a \"protocol\" column to resolve connector_id correctly",
						"point_id", pointID, "local_id", localID)
				}
			}
		}

		// Per-row connector_id column overrides everything below it.
		cid := get(row, "connector_id")
		if cid == "" {
			if v, ok := opts.ConnectorMap[proto]; ok && v != "" {
				cid = v
			} else {
				cid = opts.ConnectorID
			}
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

// inferProtocol matches localID against protocolPatterns in order and
// returns the first match's protocol name, or "" if none match.
func inferProtocol(localID string) string {
	for _, p := range protocolPatterns {
		if p.re.MatchString(localID) {
			return p.protocol
		}
	}
	return ""
}
