// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

// Offline staging for RI ADR-0009 (Proposed), pinned to RI
// 6e0fefee8be2caaa33608d76845cbab2841cf9dd. This does not apply live config.
import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	"nexus-gateway/internal/pointlist"
)

type clockNative struct {
	Protocol       string `json:"protocol"`
	GatewayID      string `json:"gatewayId"`
	ConnectorID    string `json:"connectorId"`
	DeviceInstance *int   `json:"deviceInstance"`
	ObjectType     string `json:"objectType"`
	ObjectInstance *int   `json:"objectInstance"`
	Property       string `json:"property"`
	Unit           string `json:"unit"`
}
type clockSource struct {
	ClockID         string      `json:"clockId"`
	ClockDomain     string      `json:"clockDomain"`
	Unit            string      `json:"unit"`
	ReadOnly        bool        `json:"readOnly"`
	AcquisitionMode string      `json:"acquisitionMode"`
	Native          clockNative `json:"native"`
}
type clockBinding struct {
	EquipmentID string `json:"equipmentId"`
	ClockID     string `json:"clockId"`
}
type clockSnapshot struct {
	SchemaVersion string                `json:"schemaVersion"`
	GatewayID     string                `json:"gatewayId"`
	Revision      string                `json:"revision"`
	GeneratedAt   string                `json:"generatedAt"`
	Points        []gatewayPointDTOJSON `json:"points"`
	ClockSources  []clockSource         `json:"clockSources"`
	ClockBindings []clockBinding        `json:"clockBindings"`
}

// NativeClock and NativeClockConfig contain no canonical point identifiers.
type NativeClock struct {
	ClockID       string   `json:"clock_id"`
	LocalID       string   `json:"local_id"`
	Property      string   `json:"property"`
	PointLocalIDs []string `json:"point_local_ids"`
}
type NativeClockPoint struct {
	LocalID   string `json:"local_id"`
	DeviceRef string `json:"device_ref"`
	Unit      string `json:"unit"`
	Writable  bool   `json:"writable"`
}
type NativeClockConfig struct {
	Revision    string             `json:"revision"`
	ConnectorID string             `json:"connector_id"`
	DeviceID    int                `json:"device_id"`
	Points      []NativeClockPoint `json:"points"`
	Clocks      []NativeClock      `json:"clocks"`
}

// StageClockSnapshot validates the whole immutable snapshot before producing a
// native bundle plus a separate Normalizer mapping. It deliberately supports only
// one BACnet connector/device per offline invocation; unsupported scopes fail.
func StageClockSnapshot(data []byte, gatewayID, connectorID string, deviceID int) (*NativeClockConfig, []pointlist.Entry, error) {
	fail := func(reason string) (*NativeClockConfig, []pointlist.Entry, error) {
		return nil, nil, fmt.Errorf("clock provisioning: %s", reason)
	}
	var s clockSnapshot
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return fail(err.Error())
	}
	if s.SchemaVersion != "clock-provisioning/1-draft" || gatewayID == "" || s.GatewayID != gatewayID || connectorID == "" || deviceID < 0 || deviceID > 4194302 {
		return fail("unsupported schema or scope")
	}
	// Decode the hash input without losing integer precision or optional fields.
	d = json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	decoded, err := clockJSONValue(d)
	if err != nil {
		return fail(err.Error())
	}
	if _, err := d.Token(); err != io.EOF {
		return fail("trailing JSON data")
	}
	body, ok := decoded.(map[string]any)
	if !ok {
		return fail("snapshot must be an object")
	}
	for _, key := range []string{"points", "clockSources", "clockBindings"} {
		if _, ok := body[key].([]any); !ok {
			return fail("explicit arrays required")
		}
	}
	delete(body, "revision")
	delete(body, "generatedAt")
	var canonical bytes.Buffer
	enc := json.NewEncoder(&canonical)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return fail(err.Error())
	}
	hash := sha256.Sum256(bytes.TrimSuffix(canonical.Bytes(), []byte("\n")))
	if s.Revision != fmt.Sprintf("\"sha256:%x\"", hash) {
		return fail("revision mismatch")
	}
	if !sort.SliceIsSorted(s.Points, func(i, j int) bool { return s.Points[i].PointID < s.Points[j].PointID }) ||
		!sort.SliceIsSorted(s.ClockSources, func(i, j int) bool { return s.ClockSources[i].ClockID < s.ClockSources[j].ClockID }) ||
		!sort.SliceIsSorted(s.ClockBindings, func(i, j int) bool { return s.ClockBindings[i].EquipmentID < s.ClockBindings[j].EquipmentID }) {
		return fail("noncanonical array order")
	}
	cfg := &NativeClockConfig{Revision: s.Revision, ConnectorID: connectorID, DeviceID: deviceID, Points: []NativeClockPoint{}, Clocks: []NativeClock{}}
	clocks := map[string]int{}
	clockObjects := map[string]bool{}
	for _, c := range s.ClockSources {
		n := c.Native
		if c.ClockID == "" || c.ClockDomain != "simulation" || c.Unit != "s" || !c.ReadOnly || c.AcquisitionMode != "poll" ||
			n.Protocol != "bacnet" || n.GatewayID != gatewayID || n.ConnectorID != connectorID || n.DeviceInstance == nil || *n.DeviceInstance != deviceID ||
			n.ObjectInstance == nil || *n.ObjectInstance < 0 || *n.ObjectInstance > 4194303 || (n.ObjectType != "analogValue" && n.ObjectType != "analogInput") || n.Property != "presentValue" || n.Unit != "seconds" {
			return fail("unsupported clock definition or scope")
		}
		if _, exists := clocks[c.ClockID]; exists {
			return fail("duplicate clock")
		}
		localID := fmt.Sprintf("%s,%d", n.ObjectType, *n.ObjectInstance)
		if clockObjects[localID] {
			return fail("duplicate native clock address")
		}
		clocks[c.ClockID] = len(cfg.Clocks)
		clockObjects[localID] = true
		cfg.Clocks = append(cfg.Clocks, NativeClock{ClockID: c.ClockID, LocalID: localID, Property: n.Property, PointLocalIDs: []string{}})
	}
	bindings := map[string]string{}
	for _, b := range s.ClockBindings {
		if _, ok := clocks[b.ClockID]; !ok || b.EquipmentID == "" {
			return fail("dangling binding")
		}
		if _, exists := bindings[b.EquipmentID]; exists {
			return fail("conflicting equipment binding")
		}
		bindings[b.EquipmentID] = b.ClockID
	}
	ids, nativeIDs, equipment := map[string]bool{}, map[string]bool{}, map[string]bool{}
	mapper := &HTTPClient{connectorMap: map[string]string{"bacnet": connectorID}}
	entries := []pointlist.Entry{}
	for _, p := range s.Points {
		n := p.Native
		if p.PointID == "" || ids[p.PointID] || n == nil || n.Protocol != "bacnet" || p.Protocol != "bacnet" || n.DeviceID != strconv.Itoa(deviceID) || n.ObjectType == "" {
			return fail("invalid point or unsupported scope")
		}
		instance, err := strconv.Atoi(n.InstanceNo)
		if err != nil || instance < 0 || instance > 4194303 || strconv.Itoa(instance) != n.InstanceNo {
			return fail("invalid point instance")
		}
		e := mapper.mapDTO(p)
		if nativeIDs[e.LocalID] || clockObjects[e.LocalID] || (p.LocalID != "" && p.LocalID != e.LocalID) {
			return fail("ambiguous native address or clock exposed as point")
		}
		ids[p.PointID] = true
		nativeIDs[e.LocalID] = true
		cfg.Points = append(cfg.Points, NativeClockPoint{LocalID: e.LocalID, DeviceRef: e.DeviceRef, Unit: e.Unit, Writable: e.Writable})
		entries = append(entries, e)
		if p.Device != nil {
			if len(s.ClockSources) > 0 && (p.Device.DtID == "" || p.Device.ID == "") {
				return fail("missing equipment identity")
			}
			equipment[p.Device.DtID] = true
			if c, ok := bindings[p.Device.DtID]; ok {
				i := clocks[c]
				cfg.Clocks[i].PointLocalIDs = append(cfg.Clocks[i].PointLocalIDs, e.LocalID)
			}
		} else if len(s.ClockSources) > 0 {
			return fail("missing equipment membership")
		}
	}
	for e := range bindings {
		if !equipment[e] {
			return fail("equipment has no points")
		}
	}
	for _, c := range cfg.Clocks {
		if len(c.PointLocalIDs) == 0 {
			return fail("unbound clock")
		}
	}
	return cfg, entries, nil
}

// The draft hash format omits null and permits integers only. Reject ambiguous
// duplicate keys instead of letting encoding/json silently use the last value.
func clockJSONValue(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch v := token.(type) {
	case nil:
		return nil, fmt.Errorf("null properties must be omitted")
	case json.Number:
		if _, err := strconv.ParseInt(string(v), 10, 64); err != nil {
			return nil, fmt.Errorf("static numbers must be integers")
		}
		return v, nil
	case json.Delim:
		switch v {
		case '{':
			result := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("invalid object key")
				}
				if _, exists := result[name]; exists {
					return nil, fmt.Errorf("duplicate JSON key")
				}
				value, err := clockJSONValue(d)
				if err != nil {
					return nil, err
				}
				result[name] = value
			}
			_, err := d.Token()
			return result, err
		case '[':
			result := []any{}
			for d.More() {
				value, err := clockJSONValue(d)
				if err != nil {
					return nil, err
				}
				result = append(result, value)
			}
			_, err := d.Token()
			return result, err
		}
		return nil, fmt.Errorf("unexpected delimiter")
	default:
		return token, nil
	}
}
