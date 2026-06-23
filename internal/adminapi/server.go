// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"nexus-gateway/internal/catalog"
	"nexus-gateway/internal/lifecycle"
	"nexus-gateway/internal/metrics"
	"nexus-gateway/internal/pointlist"
)

const (
	RoleOperator = "gateway-operator"
	RoleViewer   = "gateway-viewer"
)

// ConnectorManager is the lifecycle.Manager subset the Server needs.
type ConnectorManager interface {
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) error
	Upgrade(ctx context.Context, id, newImage string) error
	Rollback(ctx context.Context, id string) error
}

// ConnectorInstaller installs a connector from the Connector Catalog (ADR-0006).
// A nil Installer disables the /connectors/{name}/install endpoint.
type ConnectorInstaller interface {
	Install(ctx context.Context, name string) error
}

// CatalogSource provides catalog browsing and catalog-driven update operations (ADR-0006).
// A nil CatalogSource disables the /catalog and /connectors/{id}/update endpoints.
type CatalogSource interface {
	ListAll(ctx context.Context) ([]catalog.Manifest, error)
	Update(ctx context.Context, connectorID string) error
}

// PointListSource provides a snapshot of the synced Point List.
// A nil PointListSource disables GET /devices.
type PointListSource interface {
	Snapshot() []pointlist.Entry
}

// TelemetrySource exposes Store-and-Forward telemetry counters.
// A nil TelemetrySource disables GET /telemetry and the storefwd_* /metrics series.
type TelemetrySource interface {
	Drifts() map[string]int64
	Depth() int64
	Written() int64
	Sent() int64
	Dropped() int64
	Checkpoints() int64
	SendErrors() int64
}

// ConnectorLogger provides recent log lines for a connector container.
// A nil ConnectorLogger disables GET /logs/{id}.
type ConnectorLogger interface {
	Logs(ctx context.Context, connectorID string, tail int) ([]string, error)
}

// ServerOptions holds all optional feature sources. A nil field disables the
// corresponding endpoints. Use with NewServer (no auth) or NewSecureServer (JWT).
type ServerOptions struct {
	Installer ConnectorInstaller
	Catalog   CatalogSource
	PointList PointListSource
	Telemetry TelemetrySource
	Logger    ConnectorLogger
	// AllowAdhocUpgrade enables the dev-only POST /connectors/{id}/upgrade?image=<ref>
	// action. The MVP update path is catalog-driven (ADR-0006); when false (default)
	// the upgrade action returns 501 Not Implemented.
	AllowAdhocUpgrade bool
}

// JWTConfig configures bearer-token authentication for the Admin API. The token
// is validated against the JWKS at JWKSURL, and Audience and Issuer are enforced
// on every operator request. Keycloak/OIDC authenticates human operators here —
// a separate concern from the machine mTLS link to Building OS (ADR-0007).
type JWTConfig struct {
	JWKSURL  string
	Audience string
	Issuer   string
}


// HealthSnapshotter produces gateway health snapshots.
type HealthSnapshotter interface {
	Snapshot(ctx context.Context) lifecycle.GatewayHealth
}

// Server is the Admin HTTP API server.
type Server struct {
	mux       *http.ServeMux
	auth      *JWTMiddleware
	mgr       ConnectorManager
	installer ConnectorInstaller // nil if catalog is not configured
	catalog   CatalogSource      // nil if catalog browsing/update is not configured
	devices   PointListSource    // nil if point list is not configured
	telemetry TelemetrySource    // nil if S&F telemetry is not configured
	logger    ConnectorLogger    // nil if log streaming is not configured
	monitor   HealthSnapshotter
	shutdown  context.CancelFunc // stops the JWKS cache refresh goroutine

	allowAdhocUpgrade bool // dev-only upgrade?image= action (ADR-0006: catalog-driven by default)
}

// NewServer creates an Admin API Server with authentication DISABLED — for
// dev/local use only. Optional feature sources are supplied via opts; a nil
// field disables the corresponding endpoints.
func NewServer(mgr ConnectorManager, monitor HealthSnapshotter, opts ServerOptions) *Server {
	return buildServer(mgr, monitor, opts, &JWTMiddleware{}, false)
}

// NewSecureServer creates an Admin API Server that authenticates every operator
// request against jwt (JWKS validation + audience/issuer). Optional feature
// sources are supplied via opts. Call Shutdown() to stop the background JWKS refresh.
func NewSecureServer(mgr ConnectorManager, monitor HealthSnapshotter, opts ServerOptions, jwt JWTConfig) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	auth := &JWTMiddleware{keys: newURLKeyFetcher(ctx, jwt.JWKSURL), audience: jwt.Audience, issuer: jwt.Issuer}
	s := buildServer(mgr, monitor, opts, auth, true)
	s.shutdown = cancel
	return s
}

// Shutdown stops the JWKS cache background refresh goroutine.
func (s *Server) Shutdown() {
	if s.shutdown != nil {
		s.shutdown()
	}
}

// buildServer is the single construction path: it wires the options and registers
// routes. authenticated controls whether operator routes go through the JWT middleware.
func buildServer(mgr ConnectorManager, monitor HealthSnapshotter, opts ServerOptions, auth *JWTMiddleware, authenticated bool) *Server {
	s := &Server{
		mux:       http.NewServeMux(),
		auth:      auth,
		mgr:       mgr,
		installer: opts.Installer,
		catalog:   opts.Catalog,
		devices:   opts.PointList,
		telemetry: opts.Telemetry,
		logger:    opts.Logger,
		monitor:   monitor,

		allowAdhocUpgrade: opts.AllowAdhocUpgrade,
	}
	s.registerRoutes(authenticated)
	return s
}


func (s *Server) registerRoutes(authenticated bool) {
	require := func(role string, h http.HandlerFunc) http.HandlerFunc {
		if !authenticated {
			return h
		}
		return s.auth.require(role, h)
	}
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /connectors", require(RoleViewer, s.handleListConnectors))
	s.mux.HandleFunc("POST /connectors/{id}/{action}", require(RoleOperator, s.handleAction))
	if s.installer != nil {
		s.mux.HandleFunc("POST /connectors/{name}/install", require(RoleOperator, s.handleInstall))
	}
	if s.catalog != nil {
		s.mux.HandleFunc("GET /catalog", require(RoleViewer, s.handleListCatalog))
	}
	if s.devices != nil {
		s.mux.HandleFunc("GET /devices", require(RoleViewer, s.handleListDevices))
	}
	if s.telemetry != nil {
		s.mux.HandleFunc("GET /telemetry", require(RoleViewer, s.handleTelemetry))
	}
	if s.logger != nil {
		s.mux.HandleFunc("GET /logs/{id}", require(RoleViewer, s.handleLogs))
	}
}

func (s *Server) handleListDevices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.devices.Snapshot())
}

type logResponse struct {
	ConnectorID string   `json:"connector_id"`
	Lines       []string `json:"lines"`
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tail := 100
	if q := r.URL.Query().Get("tail"); q != "" {
		if n, err := fmt.Sscanf(q, "%d", &tail); n != 1 || err != nil || tail <= 0 {
			tail = 100
		}
	}
	lines, err := s.logger.Logs(r.Context(), id, tail)
	if err != nil {
		if errors.Is(err, lifecycle.ErrConnectorNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, logResponse{ConnectorID: id, Lines: lines})
}

type telemetryResponse struct {
	BufferDepth int64            `json:"buffer_depth"`
	Drifts      map[string]int64 `json:"drifts"`
}

func (s *Server) handleTelemetry(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, telemetryResponse{
		BufferDepth: s.telemetry.Depth(),
		Drifts:      s.telemetry.Drifts(),
	})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	h := s.monitor.Snapshot(r.Context())
	// If the handler is responding, the gateway process is live. The container
	// healthcheck (docker-compose.yml) greps the body for `"status":"ok"`.
	h.Status = "ok"
	writeJSON(w, h)
}

type connectorItem struct {
	ID          string `json:"id"`
	Image       string `json:"image"`
	PrevImage   string `json:"prev_image,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
	Running     bool   `json:"running"`
}

func (s *Server) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	h := s.monitor.Snapshot(r.Context())
	items := make([]connectorItem, 0, len(h.Connectors))
	for _, c := range h.Connectors {
		items = append(items, connectorItem{
			ID:          c.ID,
			Image:       c.Image,
			PrevImage:   c.PrevImage,
			ContainerID: c.ContainerID,
			Running:     c.Running,
		})
	}
	writeJSON(w, items)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.PathValue("action")

	var err error
	switch action {
	case "start":
		err = s.mgr.Start(r.Context(), id)
	case "stop":
		err = s.mgr.Stop(r.Context(), id)
	case "restart":
		err = s.mgr.Restart(r.Context(), id)
	case "upgrade":
		if !s.allowAdhocUpgrade {
			http.Error(w, "ad-hoc upgrade disabled; use catalog-driven update (POST /connectors/{id}/update) — ADR-0006", http.StatusNotImplemented)
			return
		}
		newImage := strings.TrimSpace(r.URL.Query().Get("image"))
		if newImage == "" {
			http.Error(w, "upgrade requires ?image=<ref>", http.StatusBadRequest)
			return
		}
		err = s.mgr.Upgrade(r.Context(), id, newImage)
	case "rollback":
		err = s.mgr.Rollback(r.Context(), id)
	case "update":
		if s.catalog == nil {
			http.Error(w, "catalog not configured", http.StatusNotImplemented)
			return
		}
		err = s.catalog.Update(r.Context(), id)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	if err != nil {
		if errors.Is(err, lifecycle.ErrConnectorNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.installer.Install(r.Context(), name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// catalogEntry is the public representation of a catalog manifest.
type catalogEntry struct {
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Image             string   `json:"image"`
	Digest            string   `json:"digest"`
	MinGatewayVersion string   `json:"min_gateway_version"`
	SignatureRequired bool     `json:"signature_required"`
	Network           []string `json:"network,omitempty"`
	Mounts            []string `json:"mounts,omitempty"`
}

func (s *Server) handleListCatalog(w http.ResponseWriter, r *http.Request) {
	manifests, err := s.catalog.ListAll(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("catalog: %v", err), http.StatusBadGateway)
		return
	}
	entries := make([]catalogEntry, len(manifests))
	for i, m := range manifests {
		entries[i] = catalogEntry{
			Name:              m.Name,
			Version:           m.Version,
			Image:             m.Image,
			Digest:            m.Digest,
			MinGatewayVersion: m.MinGatewayVersion,
			SignatureRequired: m.SignatureRequired,
			Network:           m.Permissions.Network,
			Mounts:            m.Permissions.Mounts,
		}
	}
	writeJSON(w, entries)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	h := s.monitor.Snapshot(r.Context())
	running := 0
	for _, c := range h.Connectors {
		if c.Running {
			running++
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "gateway_uptime_seconds %g\n", h.UptimeSeconds)
	fmt.Fprintf(w, "gateway_goroutines %d\n", h.GoRoutines)
	fmt.Fprintf(w, "gateway_mem_alloc_mb %g\n", h.MemAllocMB)
	fmt.Fprintf(w, "gateway_connectors_total %d\n", len(h.Connectors))
	fmt.Fprintf(w, "gateway_connectors_running %d\n", running)
	fmt.Fprintf(w, "# HELP normalizer_invalid_total Common Events the Normalizer could not parse.\n")
	fmt.Fprintf(w, "# TYPE normalizer_invalid_total counter\n")
	fmt.Fprintf(w, "normalizer_invalid_total %d\n", metrics.NormalizerInvalid())
	fmt.Fprintf(w, "# HELP normalizer_unresolved_total Common Events whose local_id is absent from the Point List.\n")
	fmt.Fprintf(w, "# TYPE normalizer_unresolved_total counter\n")
	fmt.Fprintf(w, "normalizer_unresolved_total{reason=\"point_list_miss\"} %d\n", metrics.NormalizerUnresolved())

	if s.telemetry != nil {
		t := s.telemetry
		fmt.Fprintf(w, "# HELP storefwd_buffer_depth Un-forwarded frames in the Store-and-Forward buffer (backlog beyond the cursor).\n")
		fmt.Fprintf(w, "# TYPE storefwd_buffer_depth gauge\n")
		fmt.Fprintf(w, "storefwd_buffer_depth %d\n", t.Depth())
		fmt.Fprintf(w, "# HELP storefwd_written_total Frames written to the Store-and-Forward buffer.\n")
		fmt.Fprintf(w, "# TYPE storefwd_written_total counter\n")
		fmt.Fprintf(w, "storefwd_written_total %d\n", t.Written())
		fmt.Fprintf(w, "# HELP storefwd_sent_total Frames sent up to Building OS.\n")
		fmt.Fprintf(w, "# TYPE storefwd_sent_total counter\n")
		fmt.Fprintf(w, "storefwd_sent_total %d\n", t.Sent())
		fmt.Fprintf(w, "# HELP storefwd_dropped_total Frames evicted by drop-oldest at capacity (ADR-0002).\n")
		fmt.Fprintf(w, "# TYPE storefwd_dropped_total counter\n")
		fmt.Fprintf(w, "storefwd_dropped_total %d\n", t.Dropped())
		fmt.Fprintf(w, "# HELP storefwd_checkpoint_total Successful uplink ack-checkpoints.\n")
		fmt.Fprintf(w, "# TYPE storefwd_checkpoint_total counter\n")
		fmt.Fprintf(w, "storefwd_checkpoint_total %d\n", t.Checkpoints())
		fmt.Fprintf(w, "# HELP storefwd_send_error_total Uplink send/checkpoint failures.\n")
		fmt.Fprintf(w, "# TYPE storefwd_send_error_total counter\n")
		fmt.Fprintf(w, "storefwd_send_error_total %d\n", t.SendErrors())
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data) //nolint:errcheck
}
