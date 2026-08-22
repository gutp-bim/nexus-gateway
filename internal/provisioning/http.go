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
	"strings"

	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/transport"
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

// TLSOptions configures transport security for the provisioning link (#135).
//
// The zero value keeps the historical behaviour: plain HTTP works, and ordinary
// HTTPS is verified against the host's system roots. Options are named for this
// channel rather than inherited from the gRPC link's BOS_* settings — the two
// can terminate at different edges, and an implicit inheritance would let one
// channel silently acquire credentials the operator only meant for the other.
type TLSOptions struct {
	// CAFile is a PEM bundle used to verify the server certificate.
	// Empty keeps the system roots.
	CAFile string
	// CertFile and KeyFile are the client key pair presented for mTLS.
	// Both must be set together, or neither.
	CertFile string
	KeyFile  string
	// ServerName overrides the name checked against the server certificate,
	// for dialling by IP or through an edge proxy.
	ServerName string
}

func (o TLSOptions) configured() bool {
	return o.CAFile != "" || o.CertFile != "" || o.KeyFile != "" || o.ServerName != ""
}

// NewHTTPClient creates an HTTPClient.
// connectorMap maps protocol names (e.g. "bacnet") to connector IDs (e.g. "bacnet-01").
//
// It returns an error when tlsOpts is inconsistent or its files cannot be read,
// so a misconfigured gateway fails at startup instead of surfacing later as an
// opaque 403 from the Building OS edge.
func NewHTTPClient(baseURL, gatewayID string, connectorMap map[string]string, tlsOpts TLSOptions) (*HTTPClient, error) {
	httpClient := &http.Client{}

	// Only install a custom transport when something was actually configured, so
	// the default path keeps http.DefaultTransport's connection pooling and proxy
	// behaviour untouched.
	if tlsOpts.configured() {
		// TLS settings against a non-HTTPS endpoint are inert: the CA and client
		// key pair go unused and the point list travels in clear, while the
		// configuration reads as if the link were protected. An operator who set
		// these meant to secure the channel, so refuse instead of appearing to.
		u, err := url.Parse(baseURL)
		if err != nil {
			return nil, fmt.Errorf("provisioning: base URL %q is not parseable: %w", baseURL, err)
		}
		if u.Scheme != "https" {
			return nil, fmt.Errorf(
				"provisioning: TLS options are set but the base URL scheme is %q; "+
					"use https:// or the TLS settings are ignored and requests are sent in clear",
				u.Scheme)
		}

		tlsCfg, err := transport.TLSConfig(transport.Config{
			CAFile:     tlsOpts.CAFile,
			CertFile:   tlsOpts.CertFile,
			KeyFile:    tlsOpts.KeyFile,
			ServerName: tlsOpts.ServerName,
		})
		if err != nil {
			return nil, fmt.Errorf("provisioning: TLS configuration: %w", err)
		}

		// http.DefaultTransport is a public var an embedding process may have
		// replaced; an unchecked assertion would turn that into a startup panic.
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf(
				"provisioning: cannot apply TLS settings: http.DefaultTransport is %T, not *http.Transport",
				http.DefaultTransport)
		}
		tr := base.Clone()
		tr.TLSClientConfig = tlsCfg
		httpClient.Transport = tr
	}

	return &HTTPClient{
		baseURL:      baseURL,
		gatewayID:    gatewayID,
		connectorMap: connectorMap,
		http:         httpClient,
	}, nil
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
	PointID       string                `json:"pointId"`
	LocalID       string                `json:"localId,omitempty"`
	Protocol      string                `json:"protocol,omitempty"`
	Native        *nativeAddressingJSON `json:"native,omitempty"`
	Unit          string                `json:"unit,omitempty"`
	Writable      *bool                 `json:"writable,omitempty"`
	ControlSchema *controlSchemaJSON    `json:"controlSchema,omitempty"`
	Device        *deviceRefJSON        `json:"device,omitempty"`
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

	// Protocol resolution priority: explicit top-level field wins, else the legacy BACnet-only
	// native block (older servers), else infer from the local_id's shape as a defensive fallback
	// for a server that predates the protocol field entirely — reusing the same heuristic as
	// csv.go rather than duplicating it.
	//
	// Server-supplied values are normalized (trim + lowercase) exactly as LoadCSV normalizes its
	// explicit "protocol" column: connectorMap is keyed lowercase (parseConnectorMap), so an
	// un-normalized "OPCUA" would miss the map, leave ConnectorID empty and break
	// cmd.<protocol>.<connectorID> routing. Inferred values are already lowercase.
	protocol := strings.ToLower(strings.TrimSpace(dto.Protocol))
	if protocol == "" && dto.Native != nil {
		protocol = strings.ToLower(strings.TrimSpace(dto.Native.Protocol))
	}
	if protocol == "" {
		protocol = pointlist.InferProtocol(e.LocalID)
	}
	if protocol == "" {
		protocol = "unknown"
		slog.Warn("provisioning: could not resolve protocol for point; connector routing may be wrong",
			"point_id", dto.PointID, "local_id", e.LocalID)
	}
	e.Protocol = protocol
	e.ConnectorID = c.connectorMap[protocol]

	if dto.ControlSchema != nil {
		if data, err := json.Marshal(dto.ControlSchema); err == nil {
			e.ControlSchema = string(data)
		}
	}
	return e
}
