// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/dispatch"
	"nexus-gateway/internal/egress"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
)

// TestE2E_BosReleaseDispatch uses the real RI provisioning, REST authorization,
// GatewayBridge, and audit writer. BACnet transport is replaced at the Application
// boundary by a real BACpypes commandable object. Use a dedicated local RI fixture:
// the test writes its target point and must not run against operational equipment.
func TestE2E_BosReleaseDispatch(t *testing.T) {
	if os.Getenv("E2E_BOS_RELEASE") == "" {
		t.Skip("set E2E_BOS_RELEASE=1 for RI #600 integration")
	}
	env := func(key string) string {
		t.Helper()
		v := os.Getenv(key)
		require.NotEmpty(t, v, key+" required")
		return v
	}
	api := strings.TrimRight(env("E2E_BOS_API_URL"), "/")
	bridge := env("E2E_BOS_EGRESS_ADDR")
	gateway := env("E2E_GATEWAY_ID")
	releaseID := env("E2E_RELEASE_POINT_ID")
	targetID := env("E2E_WRITABLE_POINT_ID")
	token := env("E2E_BOS_TOKEN") // admin: provisioning plus control/audit on both fixture points
	actor := env("E2E_BOS_ACTOR_SUB")
	python := env("E2E_BACNET_PYTHON")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, api+path, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, data
	}
	code, raw := request("GET", "/gateways/"+url.PathEscape(gateway)+"/pointlist", nil)
	require.Equal(t, http.StatusOK, code, "RI provisioning must succeed")
	// Feed the actual RI response unchanged into the production mapper.
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer fixture.Close()
	pl, err := provisioning.NewHTTPClient(fixture.URL, gateway, map[string]string{"bacnet": "bacnet-release"}, provisioning.TLSOptions{})
	require.NoError(t, err)
	snapshot, err := pl.Fetch(ctx, "")
	require.NoError(t, err)
	resolver := pointlist.NewSynced(nil)
	resolver.Update(snapshot.Entries)
	entry, ok := resolver.ResolveReverse(releaseID)
	require.True(t, ok)
	require.Equal(t, targetID, entry.ReleaseOf, "RI #600 must export releaseOf")
	target, ok := resolver.ResolveReverse(targetID)
	require.True(t, ok)
	require.Equal(t, "analogOutput,37", target.LocalID, "use the isolated release-test fixture")
	ns := startEmbeddedNATS(t)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	child := exec.CommandContext(ctx, python, "bacnet_release_connector.py", ns.ClientURL())
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	child.Stderr = os.Stderr
	require.NoError(t, child.Start())
	defer func() { cancel(); _ = child.Wait() }()
	scan := bufio.NewScanner(stdout)
	require.True(t, scan.Scan())
	require.Equal(t, "ready", scan.Text())
	agentCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		egress.New(bridge, gateway, dispatch.New(nc, resolver, 2*time.Second), insecureCreds(), nil).Run(agentCtx)
	}()
	defer func() { stop(); <-done }()
	post := func(point string, value int) string {
		t.Helper()
		var body []byte
		require.Eventually(t, func() bool {
			code, body = request("POST", "/api/v1/points/"+url.PathEscape(point)+"/control", []byte(fmt.Sprintf(`{"value":%d}`, value)))
			return code != http.StatusServiceUnavailable
		}, 10*time.Second, 100*time.Millisecond)
		require.Equal(t, http.StatusAccepted, code, "control must be accepted")
		var accepted struct {
			ControlID string `json:"controlId"`
		}
		require.NoError(t, json.Unmarshal(body, &accepted))
		require.NotEmpty(t, accepted.ControlID)
		return accepted.ControlID
	}
	audit := func(point, id string) {
		t.Helper()
		require.Eventually(t, func() bool {
			code, body := request("GET", "/api/v1/points/"+url.PathEscape(point)+"/control-audit?limit=50", nil)
			require.Equal(t, http.StatusOK, code)
			var rows []struct {
				ControlID string `json:"controlId"`
				PointID   string `json:"pointId"`
				Status    string `json:"status"`
				ActorSub  string `json:"actorSub"`
			}
			require.NoError(t, json.Unmarshal(body, &rows))
			for _, row := range rows {
				if row.ControlID == id {
					require.Equal(t, point, row.PointID)
					require.Equal(t, actor, row.ActorSub)
					require.NotEqual(t, "failed", row.Status, "ControlResult failed")
					return row.Status == "success"
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond, "RI must persist the returned result and actor")
	}
	state := func() (float64, int) {
		msg, err := nc.RequestWithContext(ctx, "test.bacnet.state", nil)
		require.NoError(t, err)
		var s struct {
			Value  float64
			Writes int
		}
		require.NoError(t, json.Unmarshal(msg.Data, &s))
		return s.Value, s.Writes
	}
	audit(targetID, post(targetID, 22))
	value, _ := state()
	require.Equal(t, 22.0, value)
	audit(releaseID, post(releaseID, 1))
	value, count := state()
	require.Equal(t, 18.0, value)
	require.Equal(t, 2, count)
	code, _ = request("POST", "/api/v1/points/"+url.PathEscape(releaseID)+"/control", []byte(`{"value":0}`))
	require.Equal(t, http.StatusBadRequest, code, "RI schema must reject zero before dispatch")
	_, count = state()
	require.Equal(t, 2, count)
	// An unauthenticated actor cannot release equipment.
	req, err := http.NewRequestWithContext(ctx, "POST", api+"/api/v1/points/"+url.PathEscape(releaseID)+"/control", strings.NewReader(`{"value":1}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, resp.StatusCode)
	_, count = state()
	require.Equal(t, 2, count)
}
