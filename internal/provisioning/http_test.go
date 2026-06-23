// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	PointID string              `json:"pointId"`
	LocalID string              `json:"localId,omitempty"`
	Native  *nativeAddressingDTO `json:"native,omitempty"`
	Unit    string              `json:"unit,omitempty"`
	Writable *bool              `json:"writable,omitempty"`
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

	c := provisioning.NewHTTPClient(srv.URL, "gw-test",
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

	c := provisioning.NewHTTPClient(srv.URL, "gw-test", map[string]string{})
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

	c := provisioning.NewHTTPClient(srv.URL, "gw-test",
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

	c := provisioning.NewHTTPClient(srv.URL, "gw-test", map[string]string{})
	result, err := c.Fetch(context.Background(), "etag-evicted")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Full, "full=true in diff fallback must set Full=true")
	require.Len(t, result.Entries, 1)
	assert.Equal(t, "pt-1", result.Entries[0].PointID)
	assert.Equal(t, "mqtt/device/sensor", result.Entries[0].LocalID)
}

// HTTPClient must satisfy the Client interface.
var _ provisioning.Client = (*provisioning.HTTPClient)(nil)
