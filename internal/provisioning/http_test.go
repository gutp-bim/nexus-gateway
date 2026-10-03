// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/provisioning"
)

// gatewayPointListResponse mirrors the Building OS #224 response shape.
type gatewayPointListResponse struct {
	GatewayID string            `json:"gatewayId"`
	Revision  string            `json:"revision"`
	Since     string            `json:"since,omitempty"`
	Full      bool              `json:"full"`
	Points    []gatewayPointDTO `json:"points,omitempty"`
	Added     []gatewayPointDTO `json:"added,omitempty"`
	Removed   []string          `json:"removed,omitempty"`
	Changed   []gatewayPointDTO `json:"changed,omitempty"`
}

type gatewayPointDTO struct {
	PointID  string               `json:"pointId"`
	LocalID  string               `json:"localId,omitempty"`
	Protocol string               `json:"protocol,omitempty"`
	Native   *nativeAddressingDTO `json:"native,omitempty"`
	Unit     string               `json:"unit,omitempty"`
	Writable *bool                `json:"writable,omitempty"`
}

type nativeAddressingDTO struct {
	Protocol   string `json:"protocol"`
	DeviceID   string `json:"deviceId,omitempty"`
	ObjectType string `json:"objectType,omitempty"`
	InstanceNo string `json:"instanceNo,omitempty"`
}

func boolPtr(b bool) *bool { return &b }

func TestHTTPClient_InitialFetch_ReturnsFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/gateways/gw-test/pointlist", r.URL.Path)
		assert.Empty(t, r.URL.Query().Get("since"), "initial fetch must not send ?since=")
		w.Header().Set("ETag", "etag-v1")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gatewayPointListResponse{
			GatewayID: "gw-test",
			Revision:  "etag-v1",
			Points: []gatewayPointDTO{
				{
					PointID: "supply_air_temp",
					Native: &nativeAddressingDTO{
						Protocol: "bacnet", DeviceID: "4194303",
						ObjectType: "analogInput", InstanceNo: "1001",
					},
					Unit:     "Cel",
					Writable: boolPtr(false),
				},
			},
		})
	}))
	defer srv.Close()

	c := mustHTTPClient(t, srv.URL, "gw-test",
		map[string]string{"bacnet": "bacnet-01"})

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, result, "initial fetch must return a result (not nil/304)")
	assert.True(t, result.Full, "initial fetch must return a full result")
	assert.Equal(t, "etag-v1", result.ETag)
	require.Len(t, result.Entries, 1)
	e := result.Entries[0]
	assert.Equal(t, "supply_air_temp", e.PointID)
	assert.Equal(t, "bacnet-01", e.ConnectorID)
	assert.Equal(t, "bacnet", e.Protocol)
	assert.Equal(t, "analogInput,1001", e.LocalID)
	assert.Equal(t, "4194303", e.DeviceRef)
	assert.Equal(t, "Cel", e.Unit)
	assert.False(t, e.Writable)
}

func TestHTTPClient_ETagMatch_ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "etag-v1", r.URL.Query().Get("since"), "subsequent fetch must send ?since=")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c := mustHTTPClient(t, srv.URL, "gw-test", map[string]string{})
	result, err := c.Fetch(context.Background(), "etag-v1")
	require.NoError(t, err)
	assert.Nil(t, result, "304 must return nil (unchanged)")
}

func TestHTTPClient_DiffResponse_ReturnsDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "etag-old", r.URL.Query().Get("since"))
		w.Header().Set("ETag", "etag-new")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gatewayPointListResponse{
			GatewayID: "gw-test",
			Revision:  "etag-new",
			Since:     "etag-old",
			Full:      false,
			Added: []gatewayPointDTO{
				{PointID: "new_point", Native: &nativeAddressingDTO{
					Protocol: "bacnet", ObjectType: "binaryOutput", InstanceNo: "2001",
				}, Writable: boolPtr(true)},
			},
			Removed: []string{"old_point"},
			Changed: []gatewayPointDTO{},
		})
	}))
	defer srv.Close()

	c := mustHTTPClient(t, srv.URL, "gw-test",
		map[string]string{"bacnet": "bacnet-01"})
	result, err := c.Fetch(context.Background(), "etag-old")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Full, "diff response must have Full=false")
	assert.Equal(t, "etag-new", result.ETag)
	require.Len(t, result.Added, 1)
	assert.Equal(t, "new_point", result.Added[0].PointID)
	assert.Equal(t, "binaryOutput,2001", result.Added[0].LocalID)
	assert.True(t, result.Added[0].Writable)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, "old_point", result.Removed[0])
	assert.Empty(t, result.Changed)
}

func TestHTTPClient_DiffFullFallback_ReturnsFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "etag-new")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gatewayPointListResponse{
			GatewayID: "gw-test",
			Revision:  "etag-new",
			Since:     "etag-evicted",
			Full:      true,
			Points: []gatewayPointDTO{
				{PointID: "pt-1", LocalID: "mqtt/device/sensor"},
			},
		})
	}))
	defer srv.Close()

	c := mustHTTPClient(t, srv.URL, "gw-test", map[string]string{})
	result, err := c.Fetch(context.Background(), "etag-evicted")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Full, "full=true in diff fallback must set Full=true")
	require.Len(t, result.Entries, 1)
	assert.Equal(t, "pt-1", result.Entries[0].PointID)
	assert.Equal(t, "mqtt/device/sensor", result.Entries[0].LocalID)
	assert.Equal(t, "mqtt", result.Entries[0].Protocol, "no protocol/native field — must infer from local_id shape")
}

func TestHTTPClient_ExplicitProtocolField_UsedAsIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "etag-v1")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gatewayPointListResponse{
			GatewayID: "gw-test",
			Revision:  "etag-v1",
			Points: []gatewayPointDTO{
				{PointID: "pt-opcua", LocalID: "ns=2;s=PT001", Protocol: "opcua"},
			},
		})
	}))
	defer srv.Close()

	c := mustHTTPClient(t, srv.URL, "gw-test",
		map[string]string{"opcua": "opcua-01"})

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, result.Entries, 1)
	e := result.Entries[0]
	assert.Equal(t, "opcua", e.Protocol)
	assert.Equal(t, "opcua-01", e.ConnectorID)
}

// connectorMap is keyed lowercase (parseConnectorMap), so a server sending a differently-cased or
// padded protocol must still resolve to a connector rather than silently leaving ConnectorID empty.
func TestHTTPClient_ProtocolCasingAndWhitespace_NormalizedBeforeConnectorLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "etag-v1")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gatewayPointListResponse{
			GatewayID: "gw-test",
			Revision:  "etag-v1",
			Points: []gatewayPointDTO{
				{PointID: "pt-upper", LocalID: "ns=2;s=PT001", Protocol: "  OPCUA "},
				{PointID: "pt-native", Native: &nativeAddressingDTO{
					Protocol: "BACnet", ObjectType: "analogInput", InstanceNo: "1001",
				}},
			},
		})
	}))
	defer srv.Close()

	c := mustHTTPClient(t, srv.URL, "gw-test",
		map[string]string{"opcua": "opcua-01", "bacnet": "bacnet-01"})

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, result.Entries, 2)

	assert.Equal(t, "opcua", result.Entries[0].Protocol)
	assert.Equal(t, "opcua-01", result.Entries[0].ConnectorID)

	// The legacy native.protocol fallback needs the same normalization.
	assert.Equal(t, "bacnet", result.Entries[1].Protocol)
	assert.Equal(t, "bacnet-01", result.Entries[1].ConnectorID)
}

func TestHTTPClient_NoProtocolNoNativeUnresolvableLocalID_FallsBackToUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "etag-v1")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(gatewayPointListResponse{
			GatewayID: "gw-test",
			Revision:  "etag-v1",
			Points: []gatewayPointDTO{
				{PointID: "pt-bare", LocalID: "40001"},
			},
		})
	}))
	defer srv.Close()

	c := mustHTTPClient(t, srv.URL, "gw-test", map[string]string{})

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, result.Entries, 1)
	assert.Equal(t, "unknown", result.Entries[0].Protocol)
}

// HTTPClient must satisfy the Client interface.
var _ provisioning.Client = (*provisioning.HTTPClient)(nil)

// mustHTTPClient builds a client for the plain-HTTP fixtures in this file.
// TLS options are exercised separately in http_tls_test.go (#135).
func mustHTTPClient(t *testing.T, baseURL, gatewayID string, cmap map[string]string) *provisioning.HTTPClient {
	t.Helper()
	c, err := provisioning.NewHTTPClient(baseURL, gatewayID, cmap, provisioning.TLSOptions{})
	require.NoError(t, err)
	return c
}

// liveMQTTPointJSON is the shape the live Building OS returned for an MQTT gateway
// (#174): a top-level "protocol", an explicit "native": null, and the other optional
// blocks present-but-null. Raw JSON, not the DTO structs, because omitempty would
// never emit the null.
//
// The localId deliberately has no "/": pointlist.InferProtocol classifies any id
// containing one as mqtt, which would let a mapper that ignores "protocol" pass
// anyway. Without it, only reading the top-level field yields "mqtt".
const liveMQTTPointJSON = `{
  "pointId": "pt-mqtt-temp",
  "localId": "AHU-01.current.R",
  "protocol": "mqtt",
  "native": null,
  "unit": "A",
  "writable": false,
  "controlSchema": null,
  "device": {"id": "AHU-01"}
}`

func serveRaw(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "etag-v1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A point carrying only a top-level protocol (native: null) maps to that protocol,
// gets the configured connector for it, and keeps its localId untouched (#174).
func TestHTTPClient_TopLevelProtocolWithNullNative_MapsToConfiguredConnector(t *testing.T) {
	srv := serveRaw(t, `{"gatewayId":"GW001","revision":"etag-v1","full":true,"points":[`+liveMQTTPointJSON+`]}`)
	c := mustHTTPClient(t, srv.URL, "GW001", map[string]string{"bacnet": "bacnet-01", "mqtt": "mqtt-01"})

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, result.Entries, 1)
	e := result.Entries[0]
	assert.Equal(t, "mqtt", e.Protocol)
	assert.Equal(t, "mqtt-01", e.ConnectorID)
	assert.Equal(t, "AHU-01.current.R", e.LocalID, "localId must not be rewritten")
	assert.Equal(t, "pt-mqtt-temp", e.PointID)
	assert.Equal(t, "AHU-01", e.DeviceRef)
}

// The delta path (added/changed) maps through the same code and must behave alike.
func TestHTTPClient_TopLevelProtocolWithNullNative_AlsoInDeltaResponses(t *testing.T) {
	srv := serveRaw(t, `{"gatewayId":"GW001","revision":"etag-v2","since":"etag-v1","full":false,`+
		`"added":[`+liveMQTTPointJSON+`],"changed":[`+liveMQTTPointJSON+`]}`)
	c := mustHTTPClient(t, srv.URL, "GW001", map[string]string{"mqtt": "mqtt-01"})

	result, err := c.Fetch(context.Background(), "etag-v1")
	require.NoError(t, err)
	require.Len(t, result.Added, 1)
	require.Len(t, result.Changed, 1)
	for _, e := range []struct{ name, protocol, connector string }{
		{"added", result.Added[0].Protocol, result.Added[0].ConnectorID},
		{"changed", result.Changed[0].Protocol, result.Changed[0].ConnectorID},
	} {
		assert.Equal(t, "mqtt", e.protocol, e.name)
		assert.Equal(t, "mqtt-01", e.connector, e.name)
	}
}

// Native BACnet addressing is unaffected: native.protocol still drives the protocol and
// objectType/instanceNo still compose the localId when no top-level protocol is sent.
func TestHTTPClient_NativeBACnetWithoutTopLevelProtocol_Unchanged(t *testing.T) {
	srv := serveRaw(t, `{"gatewayId":"GW001","revision":"etag-v1","full":true,"points":[`+
		`{"pointId":"pt-bac","native":{"protocol":"bacnet","deviceId":"1001","objectType":"analogInput","instanceNo":"7"}}]}`)
	c := mustHTTPClient(t, srv.URL, "GW001", map[string]string{"bacnet": "bacnet-01", "mqtt": "mqtt-01"})

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, result.Entries, 1)
	e := result.Entries[0]
	assert.Equal(t, "bacnet", e.Protocol)
	assert.Equal(t, "bacnet-01", e.ConnectorID)
	assert.Equal(t, "analogInput,7", e.LocalID)
	assert.Equal(t, "1001", e.DeviceRef)
}

// mixedPointsJSON is a Point List body with two MQTT points (top-level protocol, flat
// localIds so protocol inference cannot rescue them) and one native BACnet point.
const mixedPointsJSON = `[
  {"pointId":"pt-m1","localId":"m1","protocol":"mqtt","native":null},
  {"pointId":"pt-m2","localId":"m2","protocol":"mqtt","native":null},
  {"pointId":"pt-b1","native":{"protocol":"bacnet","deviceId":"1","objectType":"analogInput","instanceNo":"1"}}
]`

// A protocol with no CONNECTOR_MAP entry gets the fallback connector id — the same
// rule the CSV path applies and --connector-map documents — instead of an empty one
// that could never resolve an event or receive a command. Mapped protocols are
// unaffected.
func TestHTTPClient_UnmappedProtocolGetsFallbackConnectorID(t *testing.T) {
	srv := serveRaw(t, `{"gatewayId":"GW001","revision":"etag-v1","full":true,"points":`+mixedPointsJSON+`}`)
	c := mustHTTPClient(t, srv.URL, "GW001", map[string]string{"bacnet": "bacnet-01"}).
		WithFallbackConnectorID("conn-default")

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, result.Entries, 3)
	byID := map[string]string{}
	for _, e := range result.Entries {
		byID[e.PointID] = e.ConnectorID
	}
	assert.Equal(t, "conn-default", byID["pt-m1"], "mqtt has no map entry: fallback")
	assert.Equal(t, "conn-default", byID["pt-m2"])
	assert.Equal(t, "bacnet-01", byID["pt-b1"], "a mapped protocol keeps its own connector")
}

// The delta path maps through the same code.
func TestHTTPClient_UnmappedProtocolFallbackAlsoInDeltaResponses(t *testing.T) {
	srv := serveRaw(t, `{"gatewayId":"GW001","revision":"etag-v2","since":"etag-v1","full":false,"added":`+mixedPointsJSON+`}`)
	c := mustHTTPClient(t, srv.URL, "GW001", nil).WithFallbackConnectorID("mqtt-only")

	result, err := c.Fetch(context.Background(), "etag-v1")
	require.NoError(t, err)
	require.Len(t, result.Added, 3)
	for _, e := range result.Added {
		assert.Equal(t, "mqtt-only", e.ConnectorID, "an empty map falls back for every protocol: %s", e.PointID)
	}
}

// With no fallback configured the old behaviour stands (empty id), but it is no
// longer silent: it is logged — once per protocol, not once per point.
func TestHTTPClient_UnmappedProtocolWithoutFallbackIsLoggedOncePerProtocol(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := serveRaw(t, `{"gatewayId":"GW001","revision":"etag-v1","full":true,"points":`+mixedPointsJSON+`}`)
	c := mustHTTPClient(t, srv.URL, "GW001", map[string]string{"bacnet": "bacnet-01"})

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	for _, e := range result.Entries {
		if e.Protocol == "mqtt" {
			assert.Empty(t, e.ConnectorID, "no fallback configured: unchanged")
		}
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "no CONNECTOR_MAP entry"),
		"two mqtt points, one log line; the mapped bacnet point logs nothing")
	assert.Contains(t, buf.String(), "protocol=mqtt")
}

// When the fallback is used it says so, once per protocol.
func TestHTTPClient_FallbackUseIsWarnedOncePerProtocol(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := serveRaw(t, `{"gatewayId":"GW001","revision":"etag-v1","full":true,"points":`+mixedPointsJSON+`}`)
	c := mustHTTPClient(t, srv.URL, "GW001", map[string]string{"bacnet": "bacnet-01"}).
		WithFallbackConnectorID("conn-default")

	_, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	_, err = c.Fetch(context.Background(), "") // a second poll must not repeat the warning
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(buf.String(), "using the fallback connector id"))
	assert.Contains(t, buf.String(), "connector_id=conn-default")
}
