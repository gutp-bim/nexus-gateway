// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"nexus-gateway/internal/pointlist"
)

// HTTPClient implements Client against the real Building OS provisioning API (#224).
// Endpoint: GET /gateways/{gatewayID}/pointlist
// Uses ETag / If-None-Match (304) for version check and ?since={etag} for diffs.
type HTTPClient struct {
	baseURL      string
	gatewayID    string
	connectorMap map[string]string // protocol → connectorID
	http         *http.Client
}

// NewHTTPClient creates an HTTPClient.
// connectorMap maps protocol names (e.g. "bacnet") to connector IDs (e.g. "bacnet-01").
func NewHTTPClient(baseURL, gatewayID string, connectorMap map[string]string) *HTTPClient {
	return &HTTPClient{
		baseURL:      baseURL,
		gatewayID:    gatewayID,
		connectorMap: connectorMap,
		http:         &http.Client{},
	}
}

// Fetch implements Client. Returns nil on 304 (point list unchanged).
func (c *HTTPClient) Fetch(ctx context.Context, knownETag string) (*FetchResult, error) {
	urlStr := fmt.Sprintf("%s/gateways/%s/pointlist",
		c.baseURL, url.PathEscape(c.gatewayID))
	if knownETag != "" {
		urlStr += "?since=" + url.QueryEscape(knownETag)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if knownETag != "" {
		req.Header.Set("If-None-Match", knownETag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body) // drain to allow connection reuse
		return nil, fmt.Errorf("provisioning: status %d", resp.StatusCode)
	}

	var body gatewayPointListResponseJSON
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("provisioning: decode: %w", err)
	}

	etag := resp.Header.Get("ETag")
	if etag == "" {
		etag = body.Revision
	}
	if etag == "" {
		slog.Warn("provisioning: server returned no ETag or revision — will refetch on next poll")
	}

	// A full response when: no ?since= was sent (initial fetch), or server set full=true (evicted base).
	isFull := knownETag == "" || body.Full
	result := &FetchResult{ETag: etag, Full: isFull}
	if isFull {
		result.Entries = c.mapDTOs(body.Points)
	} else {
		result.Added = c.mapDTOs(body.Added)
		result.Removed = body.Removed
		result.Changed = c.mapDTOs(body.Changed)
	}
	return result, nil
}

// ── JSON types mirroring the Building OS #224 wire format ───────────────────

type gatewayPointListResponseJSON struct {
	GatewayID string                `json:"gatewayId"`
	Revision  string                `json:"revision"`
	Since     string                `json:"since,omitempty"`
	Full      bool                  `json:"full"`
	Points    []gatewayPointDTOJSON `json:"points,omitempty"`
	Added     []gatewayPointDTOJSON `json:"added,omitempty"`
	Removed   []string              `json:"removed,omitempty"`
	Changed   []gatewayPointDTOJSON `json:"changed,omitempty"`
}

type gatewayPointDTOJSON struct {
	PointID      string                 `json:"pointId"`
	LocalID      string                 `json:"localId,omitempty"`
	Native       *nativeAddressingJSON  `json:"native,omitempty"`
	Unit         string                 `json:"unit,omitempty"`
	Writable     *bool                  `json:"writable,omitempty"`
	ControlSchema *controlSchemaJSON    `json:"controlSchema,omitempty"`
	Device       *deviceRefJSON         `json:"device,omitempty"`
}

type nativeAddressingJSON struct {
	Protocol   string `json:"protocol"`
	DeviceID   string `json:"deviceId,omitempty"`
	ObjectType string `json:"objectType,omitempty"`
	InstanceNo string `json:"instanceNo,omitempty"`
}

type controlSchemaJSON struct {
	DataType   string `json:"dataType,omitempty"`
	MinValue   string `json:"minValue,omitempty"`
	MaxValue   string `json:"maxValue,omitempty"`
	EnumLabels string `json:"enumLabels,omitempty"`
}

type deviceRefJSON struct {
	DtID string `json:"dtId,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func (c *HTTPClient) mapDTOs(dtos []gatewayPointDTOJSON) []pointlist.Entry {
	entries := make([]pointlist.Entry, 0, len(dtos))
	for _, dto := range dtos {
		entries = append(entries, c.mapDTO(dto))
	}
	return entries
}

func (c *HTTPClient) mapDTO(dto gatewayPointDTOJSON) pointlist.Entry {
	e := pointlist.Entry{
		PointID:  dto.PointID,
		Unit:     dto.Unit,
		Writable: dto.Writable != nil && *dto.Writable,
	}

	if dto.Native != nil {
		e.Protocol = dto.Native.Protocol
		e.ConnectorID = c.connectorMap[dto.Native.Protocol]
		// Match the local_id convention from csv.go: "objectType,instanceNo"
		if dto.Native.ObjectType != "" || dto.Native.InstanceNo != "" {
			e.LocalID = dto.Native.ObjectType + "," + dto.Native.InstanceNo
		} else {
			e.LocalID = dto.LocalID
		}
		e.DeviceRef = dto.Native.DeviceID
	} else {
		e.LocalID = dto.LocalID
	}

	if dto.Device != nil && e.DeviceRef == "" {
		e.DeviceRef = dto.Device.ID
	}

	if dto.ControlSchema != nil {
		if data, err := json.Marshal(dto.ControlSchema); err == nil {
			e.ControlSchema = string(data)
		}
	}
	return e
}
