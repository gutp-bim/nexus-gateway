// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package adminapi_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/adminapi"
	"nexus-gateway/internal/catalog"
	"nexus-gateway/internal/lifecycle"
	"nexus-gateway/internal/metrics"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/version"
)

// parseMetricInt extracts the integer value of a single (unlabeled) Prometheus
// series line "name <int>" from an exposition body.
func parseMetricInt(t *testing.T, body, name string) int64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+" ") {
			var v int64
			_, err := fmt.Sscanf(strings.TrimPrefix(line, name+" "), "%d", &v)
			require.NoError(t, err)
			return v
		}
	}
	t.Fatalf("metric %q not found in body", name)
	return 0
}

// ── helpers ──────────────────────────────────────────────────────────────────

type testFixture struct {
	privKey    *rsa.PrivateKey
	jwksServer *httptest.Server
	srv        *adminapi.Server
	apiServer  *httptest.Server
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pub, err := jwk.PublicKeyOf(privKey)
	require.NoError(t, err)
	require.NoError(t, pub.Set(jwk.KeyIDKey, "test-key"))
	require.NoError(t, pub.Set(jwk.AlgorithmKey, jwa.RS256))

	set := jwk.NewSet()
	require.NoError(t, set.AddKey(pub))

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(set) //nolint:errcheck
	}))
	t.Cleanup(jwksServer.Close)

	mgr := &mockManager{}
	mon := &mockMonitor{}
	srv := adminapi.NewSecureServer(mgr, mon, adminapi.ServerOptions{},
		adminapi.JWTConfig{JWKSURL: jwksServer.URL, Audience: "nexus-gateway", Issuer: "test-issuer"})
	t.Cleanup(srv.Shutdown)
	apiServer := httptest.NewServer(srv)
	t.Cleanup(apiServer.Close)

	return &testFixture{
		privKey:    privKey,
		jwksServer: jwksServer,
		srv:        srv,
		apiServer:  apiServer,
	}
}

func (f *testFixture) signToken(t *testing.T, roles []string, expiry time.Time) string {
	t.Helper()
	return signToken(t, f.privKey, "test-issuer", "nexus-gateway", roles, expiry)
}

// signToken builds and signs a JWT with configurable issuer and audience.
func signToken(t *testing.T, privKey *rsa.PrivateKey, issuer, audience string, roles []string, expiry time.Time) string {
	t.Helper()
	b := jwt.NewBuilder().
		Issuer(issuer).
		Audience([]string{audience}).
		Expiration(expiry).
		Claim("realm_access", map[string]any{"roles": roles})
	tok, err := b.Build()
	require.NoError(t, err)
	priv, err := jwk.FromRaw(privKey)
	require.NoError(t, err)
	require.NoError(t, priv.Set(jwk.KeyIDKey, "test-key"))
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, priv))
	require.NoError(t, err)
	return string(signed)
}

func (f *testFixture) get(path, token string) *http.Response {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.apiServer.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	return resp
}

func (f *testFixture) post(path, token string) *http.Response {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, f.apiServer.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	return resp
}

// ── mocks ──────────────────────────────────────────────────────────────────

type mockManager struct {
	lastAction string
	lastID     string
	err        error
}

func (m *mockManager) Start(_ context.Context, id string) error {
	m.lastAction, m.lastID = "start", id
	return m.err
}
func (m *mockManager) Stop(_ context.Context, id string) error {
	m.lastAction, m.lastID = "stop", id
	return m.err
}
func (m *mockManager) Restart(_ context.Context, id string) error {
	m.lastAction, m.lastID = "restart", id
	return m.err
}
func (m *mockManager) Upgrade(_ context.Context, id, _ string) error {
	m.lastAction, m.lastID = "upgrade", id
	return m.err
}
func (m *mockManager) Rollback(_ context.Context, id string) error {
	m.lastAction, m.lastID = "rollback", id
	return m.err
}

type mockPointListSource struct {
	entries []pointlist.Entry
}

func (m *mockPointListSource) Snapshot() []pointlist.Entry { return m.entries }

type mockCatalogSource struct {
	manifests  []catalog.Manifest
	lastUpdate string
	err        error
}

func (m *mockCatalogSource) ListAll(_ context.Context) ([]catalog.Manifest, error) {
	return m.manifests, m.err
}
func (m *mockCatalogSource) Update(_ context.Context, id string) error {
	m.lastUpdate = id
	return m.err
}

type mockInstaller struct {
	lastInstall string
	err         error
}

func (m *mockInstaller) Install(_ context.Context, name string) error {
	m.lastInstall = name
	return m.err
}

type mockMonitor struct{}

func (m *mockMonitor) Snapshot(_ context.Context) lifecycle.GatewayHealth {
	return lifecycle.GatewayHealth{
		UptimeSeconds: 42.0,
		GoRoutines:    5,
		MemAllocMB:    1.5,
		Connectors: []lifecycle.ConnectorHealth{
			{ID: "mqtt-01", Running: true},
		},
	}
}

// ── auth tests ────────────────────────────────────────────────────────────

func TestAuth_NoToken_Returns401(t *testing.T) {
	f := newFixture(t)
	resp := f.get("/connectors", "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_ExpiredToken_Returns401(t *testing.T) {
	f := newFixture(t)
	tok := f.signToken(t, []string{adminapi.RoleViewer}, time.Now().Add(-1*time.Hour))
	resp := f.get("/connectors", tok)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_WrongAudience_Returns401(t *testing.T) {
	f := newFixture(t)
	tok := signToken(t, f.privKey, "test-issuer", "wrong-audience", []string{adminapi.RoleViewer}, time.Now().Add(1*time.Hour))
	resp := f.get("/connectors", tok)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_WrongIssuer_Returns401(t *testing.T) {
	f := newFixture(t)
	tok := signToken(t, f.privKey, "evil-realm", "nexus-gateway", []string{adminapi.RoleViewer}, time.Now().Add(1*time.Hour))
	resp := f.get("/connectors", tok)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_ViewerCanListConnectors(t *testing.T) {
	f := newFixture(t)
	tok := f.signToken(t, []string{adminapi.RoleViewer}, time.Now().Add(1*time.Hour))
	resp := f.get("/connectors", tok)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAuth_OperatorCanListConnectors(t *testing.T) {
	f := newFixture(t)
	tok := f.signToken(t, []string{adminapi.RoleOperator}, time.Now().Add(1*time.Hour))
	resp := f.get("/connectors", tok)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAuth_ViewerCannotRestart_Returns403(t *testing.T) {
	f := newFixture(t)
	tok := f.signToken(t, []string{adminapi.RoleViewer}, time.Now().Add(1*time.Hour))
	resp := f.post("/connectors/mqtt-01/restart", tok)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAuth_OperatorCanRestart(t *testing.T) {
	f := newFixture(t)
	tok := f.signToken(t, []string{adminapi.RoleOperator}, time.Now().Add(1*time.Hour))
	resp := f.post("/connectors/mqtt-01/restart", tok)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// ── endpoint tests ───────────────────────────────────────────────────────

func TestHealth_NoAuthRequired(t *testing.T) {
	f := newFixture(t)
	resp := f.get("/health", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var h lifecycle.GatewayHealth
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&h))
	assert.Greater(t, h.UptimeSeconds, 0.0)
}

// The container healthcheck greps the liveness route for `"status":"ok"`; it must
// always report ok (process serving) regardless of degraded readiness (#45).
func TestHealthLive_AlwaysOK(t *testing.T) {
	metrics.SetNatsConnected(false) // even with a degraded /health, liveness stays ok
	t.Cleanup(func() { metrics.SetNatsConnected(false) })

	f := newFixture(t)
	resp := f.get("/health/live", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "ok", body["status"], "liveness must always report ok for the container healthcheck")
}

// /health reports degraded (still HTTP 200) when a component is unhealthy — here,
// NATS disconnected — with a named component reason (#45).
func TestHealth_DegradedWhenNatsDown(t *testing.T) {
	metrics.SetNatsConnected(false)
	t.Cleanup(func() { metrics.SetNatsConnected(false) })

	f := newFixture(t)
	resp := f.get("/health", "")
	require.Equal(t, http.StatusOK, resp.StatusCode, "degraded is still HTTP 200")
	var h lifecycle.GatewayHealth
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&h))
	assert.Equal(t, "degraded", h.Status)
	var natsDown bool
	for _, c := range h.Components {
		if c.Name == "nats" {
			natsDown = c.Status == "degraded" && c.Reason != ""
		}
	}
	assert.True(t, natsDown, "the nats component must be degraded with a reason")
}

// /health reports ok when every evaluated component is healthy.
func TestHealth_OKWhenAllComponentsHealthy(t *testing.T) {
	metrics.SetNatsConnected(true)
	metrics.SetUplinkConnected(true)
	t.Cleanup(func() { metrics.SetNatsConnected(false); metrics.SetUplinkConnected(false) })

	// mockMonitor reports one running connector; no telemetry/devices sources are
	// wired, so those components are omitted — leaving nats + connectors, both ok.
	f := newFixture(t)
	resp := f.get("/health", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var h lifecycle.GatewayHealth
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&h))
	assert.Equal(t, "ok", h.Status)
}

func TestConnectors_ReturnsConnectorList(t *testing.T) {
	f := newFixture(t)
	tok := f.signToken(t, []string{adminapi.RoleViewer}, time.Now().Add(1*time.Hour))
	resp := f.get("/connectors", tok)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var items []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	require.Len(t, items, 1)
	assert.Equal(t, "mqtt-01", items[0]["id"])
	assert.Equal(t, true, items[0]["running"])
}

func TestAction_Start(t *testing.T) {
	f := newFixture(t)
	mgr := &mockManager{}
	mon := &mockMonitor{}
	srv := adminapi.NewSecureServer(mgr, mon, adminapi.ServerOptions{},
		adminapi.JWTConfig{JWKSURL: f.jwksServer.URL, Audience: "nexus-gateway", Issuer: "test-issuer"})
	t.Cleanup(srv.Shutdown)
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	tok := f.signToken(t, []string{adminapi.RoleOperator}, time.Now().Add(1*time.Hour))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, apiSrv.URL+"/connectors/mqtt-01/start", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "start", mgr.lastAction)
	assert.Equal(t, "mqtt-01", mgr.lastID)
}

func TestAction_UnknownConnector_Returns404(t *testing.T) {
	f := newFixture(t)
	mgr := &mockManager{err: fmt.Errorf("lifecycle: connector %q: %w", "ghost", lifecycle.ErrConnectorNotFound)}
	mon := &mockMonitor{}
	srv := adminapi.NewSecureServer(mgr, mon, adminapi.ServerOptions{},
		adminapi.JWTConfig{JWKSURL: f.jwksServer.URL, Audience: "nexus-gateway", Issuer: "test-issuer"})
	t.Cleanup(srv.Shutdown)
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	tok := f.signToken(t, []string{adminapi.RoleOperator}, time.Now().Add(1*time.Hour))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, apiSrv.URL+"/connectors/ghost/restart", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAction_UnknownAction_Returns400(t *testing.T) {
	f := newFixture(t)
	tok := f.signToken(t, []string{adminapi.RoleOperator}, time.Now().Add(1*time.Hour))
	resp := f.post("/connectors/mqtt-01/explode", tok)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ── ad-hoc upgrade fence tests (#75) ─────────────────────────────────────────

// By default the MVP update path is catalog-driven; the ad-hoc
// `upgrade?image=<ref>` action is disabled and returns 501.
func TestAction_AdhocUpgradeDisabledByDefault(t *testing.T) {
	mgr := &mockManager{}
	srv := adminapi.NewServer(mgr, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		apiSrv.URL+"/connectors/mqtt-01/upgrade?image=ghcr.io/x@sha256:abc", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	assert.Empty(t, mgr.lastAction, "Upgrade must not be invoked when ad-hoc upgrade is disabled")
}

// With AllowAdhocUpgrade enabled (dev), the action proceeds to the manager.
func TestAction_AdhocUpgradeAllowedWithFlag(t *testing.T) {
	mgr := &mockManager{}
	srv := adminapi.NewServer(mgr, &mockMonitor{}, adminapi.ServerOptions{AllowAdhocUpgrade: true})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		apiSrv.URL+"/connectors/mqtt-01/upgrade?image=ghcr.io/x@sha256:abc", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "upgrade", mgr.lastAction)
}

// ── capabilities tests (#40) ─────────────────────────────────────────────────

// The Admin UI reads GET /capabilities to decide whether to offer the free-form
// ad-hoc image field in the Upgrade dialog. Default: ad-hoc upgrade disabled.
func TestCapabilities_AdhocDisabledByDefault(t *testing.T) {
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/capabilities")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var caps map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&caps))
	assert.Equal(t, false, caps["allow_adhoc_upgrade"])
}

// With AllowAdhocUpgrade enabled (dev), the capability is advertised as true so
// the UI reveals the ad-hoc image field.
func TestCapabilities_AdhocReportedWhenEnabled(t *testing.T) {
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{AllowAdhocUpgrade: true})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/capabilities")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var caps map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&caps))
	assert.Equal(t, true, caps["allow_adhoc_upgrade"])
}

// ── devices tests ────────────────────────────────────────────────────────────

func TestDevices_ListAll(t *testing.T) {
	src := &mockPointListSource{entries: []pointlist.Entry{
		{ConnectorID: "bacnet-01", Protocol: "bacnet", LocalID: "AHU-1/sup_temp", PointID: "p-001", Unit: "Cel", DeviceRef: "ahu-01"},
		{ConnectorID: "bacnet-01", Protocol: "bacnet", LocalID: "AHU-1/fan_run", PointID: "p-002", Writable: true, DeviceRef: "ahu-01"},
	}}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{PointList: src})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/devices")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var items []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	require.Len(t, items, 2)
	assert.Equal(t, "p-001", items[0]["point_id"])
	assert.Equal(t, "bacnet-01", items[0]["connector_id"])
	assert.Equal(t, "Cel", items[0]["unit"])
	assert.Equal(t, true, items[1]["writable"])
}

func TestDevices_NilSource_Returns404(t *testing.T) {
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, _ := http.Get(apiSrv.URL + "/devices")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ── telemetry tests ──────────────────────────────────────────────────────────

type mockTelemetrySource struct {
	drifts         map[string]int64
	depth          int64
	written        int64
	sent           int64
	accepted       int64
	dropped        int64
	writeErrors    int64
	capacity       int
	checkpoints    int64
	sendErrors     int64
	driftTotal     int64
	lastCheckpoint int64
}

func (m *mockTelemetrySource) Drifts() map[string]int64  { return m.drifts }
func (m *mockTelemetrySource) Depth() int64              { return m.depth }
func (m *mockTelemetrySource) Written() int64            { return m.written }
func (m *mockTelemetrySource) Sent() int64               { return m.sent }
func (m *mockTelemetrySource) Accepted() int64           { return m.accepted }
func (m *mockTelemetrySource) Dropped() int64            { return m.dropped }
func (m *mockTelemetrySource) WriteErrors() int64        { return m.writeErrors }
func (m *mockTelemetrySource) Capacity() int             { return m.capacity }
func (m *mockTelemetrySource) Checkpoints() int64        { return m.checkpoints }
func (m *mockTelemetrySource) SendErrors() int64         { return m.sendErrors }
func (m *mockTelemetrySource) DriftTotal() int64         { return m.driftTotal }
func (m *mockTelemetrySource) LastCheckpointUnix() int64 { return m.lastCheckpoint }

// mockStreamStats is a fake adminapi.StreamStatSource for the telemetry payload.
type mockStreamStats struct {
	msgs  uint64
	bytes uint64
	err   error
}

func (m mockStreamStats) StreamStats(context.Context) (uint64, uint64, error) {
	return m.msgs, m.bytes, m.err
}

// /metrics must expose the store-and-forward series when a TelemetrySource is wired.
func TestMetrics_IncludesStorefwd(t *testing.T) {
	// depth>0 (pending backlog) so the checkpoint timestamp reports the actual
	// stored value rather than the "now" freshness override (#23).
	src := &mockTelemetrySource{
		depth: 12, written: 1043, sent: 1031, dropped: 4, writeErrors: 2, checkpoints: 34, sendErrors: 1,
		driftTotal: 7, lastCheckpoint: 1700000000,
	}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{Telemetry: src})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/metrics")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	for _, want := range []string{
		"storefwd_buffer_depth 12",
		"storefwd_written_total 1043",
		"storefwd_sent_total 1031",
		"storefwd_dropped_total 4",
		"storefwd_write_error_total 2",
		"storefwd_checkpoint_total 34",
		"storefwd_send_error_total 1",
		"storefwd_drift_total 7",
		"storefwd_last_checkpoint_timestamp_seconds 1700000000",
		"# TYPE storefwd_written_total counter",
		"# TYPE storefwd_buffer_depth gauge",
		"# TYPE storefwd_drift_total counter",
		"# TYPE storefwd_last_checkpoint_timestamp_seconds gauge",
	} {
		assert.Contains(t, body, want)
	}
}

// With an empty backlog the checkpoint-staleness series reports "now" so a quiet,
// healthy gateway does not look stale — staleness accrues only while frames pend (#23).
func TestMetrics_CheckpointFreshWhenBacklogEmpty(t *testing.T) {
	// depth 0, stale stored checkpoint (long ago). Exposition must override to ~now.
	src := &mockTelemetrySource{depth: 0, lastCheckpoint: 1000}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{Telemetry: src})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/metrics")
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	assert.NotContains(t, body, "storefwd_last_checkpoint_timestamp_seconds 1000",
		"empty backlog must not report the stale stored checkpoint")
	ts := parseMetricInt(t, body, "storefwd_last_checkpoint_timestamp_seconds")
	assert.Greater(t, ts, int64(1_600_000_000), "should report a recent (now-ish) timestamp")
}

// /metrics must expose per-connector up gauges and the NATS/uplink connectivity gauges (#23/#24).
func TestMetrics_IncludesConnectivityAndConnectorUp(t *testing.T) {
	metrics.SetNatsConnected(true)
	metrics.SetUplinkConnected(false)
	t.Cleanup(func() { metrics.SetNatsConnected(false); metrics.SetUplinkConnected(false) })

	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/metrics")
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	for _, want := range []string{
		`gateway_connector_up{connector_id="mqtt-01"} 1`,
		"# TYPE gateway_connector_up gauge",
		"nats_connected 1",
		"uplink_connected 0",
		"gateway_cpu_percent ",
	} {
		assert.Contains(t, body, want)
	}
}

// /metrics must expose gateway_build_info carrying the single-source version (#22).
func TestMetrics_IncludesBuildInfo(t *testing.T) {
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/metrics")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	assert.Contains(t, body, "# TYPE gateway_build_info gauge")
	assert.Contains(t, body, "gateway_build_info{version=\""+version.String()+"\"} 1")
}

// /metrics must still work (and omit storefwd_*) when no TelemetrySource is wired.
func TestMetrics_OmitsStorefwdWhenNoSource(t *testing.T) {
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/metrics")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	b, _ := io.ReadAll(resp.Body)
	assert.NotContains(t, string(b), "storefwd_")
	assert.Contains(t, string(b), "gateway_uptime_seconds")
}

func TestTelemetry_ReturnsDriftAndDepth(t *testing.T) {
	src := &mockTelemetrySource{
		drifts: map[string]int64{"p-001": 3, "p-002": 0},
		depth:  42,
	}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{Telemetry: src})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/telemetry")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, float64(42), body["buffer_depth"])
	drifts, ok := body["drifts"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(3), drifts["p-001"])
	assert.Equal(t, float64(0), drifts["p-002"])
}

// The telemetry payload is a single document carrying the full pipeline figures
// plus EVENTS stream usage and uplink state (#47).
func TestTelemetry_ExtendedPayload(t *testing.T) {
	metrics.SetUplinkConnected(true)
	t.Cleanup(func() { metrics.SetUplinkConnected(false) })

	src := &mockTelemetrySource{
		drifts: map[string]int64{"p-001": 2}, depth: 5,
		written: 1000, sent: 990, accepted: 988, dropped: 3, checkpoints: 20, sendErrors: 1,
		driftTotal: 2, lastCheckpoint: 1700000000,
	}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{
		Telemetry:   src,
		StreamStats: mockStreamStats{msgs: 4321, bytes: 987654},
	})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/telemetry")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, float64(1000), body["received"])
	assert.Equal(t, float64(990), body["sent"])
	assert.Equal(t, float64(988), body["accepted"])
	assert.Equal(t, float64(5), body["buffer_depth"])
	assert.Equal(t, float64(3), body["dropped"])
	assert.Equal(t, float64(20), body["checkpoints"])
	assert.Equal(t, float64(1), body["send_errors"])
	assert.Equal(t, float64(2), body["drift_total"])
	assert.Equal(t, true, body["uplink_connected"])
	assert.Equal(t, float64(1700000000), body["last_checkpoint_unix"])
	stream, ok := body["events_stream"].(map[string]any)
	require.True(t, ok, "events_stream present when a StreamStatSource is wired")
	assert.Equal(t, float64(4321), stream["msgs"])
	assert.Equal(t, float64(987654), stream["bytes"])
}

// A JetStream error (or no StreamStatSource) omits events_stream but still serves
// the rest of the payload — the stream figure is best-effort.
func TestTelemetry_OmitsEventsStreamOnError(t *testing.T) {
	src := &mockTelemetrySource{depth: 1}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{
		Telemetry:   src,
		StreamStats: mockStreamStats{err: errors.New("jetstream down")},
	})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/telemetry")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	_, present := body["events_stream"]
	assert.False(t, present, "events_stream omitted when the stream source errors")
	assert.Equal(t, float64(1), body["buffer_depth"])
}

func TestTelemetry_NilSource_Returns404(t *testing.T) {
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, _ := http.Get(apiSrv.URL + "/telemetry")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ── canonical constructor tests (NewServer / NewSecureServer) ─────────────────

// NewServer is the no-auth constructor: optional sources via ServerOptions,
// every endpoint open. It must register optional routes from the options.
func TestNewServer_RegistersOptionalRoutes(t *testing.T) {
	src := &mockTelemetrySource{drifts: map[string]int64{"p1": 1}, depth: 7}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{Telemetry: src})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/telemetry")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// NewSecureServer is the JWT constructor: operator endpoints reject requests
// that carry no bearer token.
func TestNewSecureServer_RejectsUnauthenticated(t *testing.T) {
	f := newFixture(t)
	srv := adminapi.NewSecureServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{},
		adminapi.JWTConfig{JWKSURL: f.jwksServer.URL, Audience: "nexus-gateway", Issuer: "test-issuer"})
	t.Cleanup(srv.Shutdown)
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/connectors")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// ── logs tests ───────────────────────────────────────────────────────────────

type mockConnectorLogger struct {
	lines map[string][]string
	err   error
}

func (m *mockConnectorLogger) Logs(_ context.Context, id string, _ int) ([]string, error) {
	return m.lines[id], m.err
}

func TestLogs_ReturnsLinesForConnector(t *testing.T) {
	lg := &mockConnectorLogger{
		lines: map[string][]string{
			"bacnet-01": {"2026-06-15 INFO starting", "2026-06-15 WARN reconnecting"},
		},
	}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{Logger: lg})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/logs/bacnet-01")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "bacnet-01", body["connector_id"])
	lines, ok := body["lines"].([]any)
	require.True(t, ok)
	assert.Len(t, lines, 2)
	assert.Equal(t, "2026-06-15 INFO starting", lines[0])
}

func TestLogs_UnknownConnector_Returns404(t *testing.T) {
	lg := &mockConnectorLogger{err: fmt.Errorf("lifecycle: connector %q: %w", "ghost", lifecycle.ErrConnectorNotFound)}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{Logger: lg})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, _ := http.Get(apiSrv.URL + "/logs/ghost")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestLogs_NilSource_Returns404(t *testing.T) {
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, _ := http.Get(apiSrv.URL + "/logs/bacnet-01")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ── catalog tests ────────────────────────────────────────────────────────────

func TestCatalog_ListAll_NoAuth(t *testing.T) {
	src := &mockCatalogSource{
		manifests: []catalog.Manifest{
			{Name: "sim-connector", Version: "1.0.0", Image: "ghcr.io/org/sim-connector:v1.0.0", Digest: "sha256:abc123"},
		},
	}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{Installer: &mockInstaller{}, Catalog: src})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, err := http.Get(apiSrv.URL + "/catalog")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var entries []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "sim-connector", entries[0]["name"])
	assert.Equal(t, "1.0.0", entries[0]["version"])
	assert.Equal(t, "sha256:abc123", entries[0]["digest"])
}

func TestCatalog_NilSource_Returns404(t *testing.T) {
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	resp, _ := http.Get(apiSrv.URL + "/catalog")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestCatalog_UpdateAction_CallsCatalogSource(t *testing.T) {
	src := &mockCatalogSource{}
	srv := adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{Installer: &mockInstaller{}, Catalog: src})
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, apiSrv.URL+"/connectors/sim-connector/update", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "sim-connector", src.lastUpdate)
}

func TestCatalog_JWTPath_ListAll(t *testing.T) {
	f := newFixture(t)
	src := &mockCatalogSource{
		manifests: []catalog.Manifest{
			{Name: "bacnet-connector", Version: "2.0.0", Image: "ghcr.io/org/bacnet:v2.0.0", Digest: "sha256:deadbeef"},
		},
	}
	mgr := &mockManager{}
	mon := &mockMonitor{}
	srv := adminapi.NewSecureServer(mgr, mon, adminapi.ServerOptions{Installer: &mockInstaller{}, Catalog: src},
		adminapi.JWTConfig{JWKSURL: f.jwksServer.URL, Audience: "nexus-gateway", Issuer: "test-issuer"})
	t.Cleanup(srv.Shutdown)
	apiSrv := httptest.NewServer(srv)
	t.Cleanup(apiSrv.Close)

	tok := f.signToken(t, []string{adminapi.RoleViewer}, time.Now().Add(1*time.Hour))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, apiSrv.URL+"/catalog", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var entries []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "bacnet-connector", entries[0]["name"])
}

func TestMetrics_NoAuthRequired(t *testing.T) {
	f := newFixture(t)
	resp := f.get("/metrics", "")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	bodyBytes, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(bodyBytes), "gateway_uptime_seconds")
	assert.Contains(t, string(bodyBytes), "gateway_goroutines")
	assert.Contains(t, string(bodyBytes), "normalizer_invalid_total")
	assert.Contains(t, string(bodyBytes), "normalizer_unresolved_total")
}
