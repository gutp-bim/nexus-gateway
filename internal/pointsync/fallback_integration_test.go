// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointsync_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/pointsync"
	"nexus-gateway/internal/provisioning"
)

// gatewayPointListResponse/gatewayPointDTO mirror the real Building OS #224
// wire shape closely enough for this test (see http_test.go for the fuller,
// authoritative fixture of the same shapes).
type fallbackITPointListResponse struct {
	GatewayID string               `json:"gatewayId"`
	Revision  string               `json:"revision"`
	Full      bool                 `json:"full"`
	Points    []fallbackITPointDTO `json:"points,omitempty"`
}

type fallbackITPointDTO struct {
	PointID  string `json:"pointId"`
	LocalID  string `json:"localId,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

// TestFallbackClient_EndToEnd_FileBootstrapThenPromotesToBuildingOS is EP-013's
// scoped integration test: the gateway starts on --provisioning-mode=fallback
// against an unreachable Building OS provisioning endpoint and a valid
// --provisioning-file, resolves from the file, then converges to Building
// OS's point list once it becomes reachable — without a process restart.
func TestFallbackClient_EndToEnd_FileBootstrapThenPromotesToBuildingOS(t *testing.T) {
	var bosUp atomic.Bool // Building OS's provisioning API starts down.

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !bosUp.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("ETag", "bos-etag-1")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fallbackITPointListResponse{
			GatewayID: "gw-test",
			Revision:  "bos-etag-1",
			Full:      true,
			Points: []fallbackITPointDTO{
				{PointID: "bos_point", LocalID: "sensors/bos", Protocol: "mqtt"},
			},
		})
	}))
	defer srv.Close()

	httpClient, err := provisioning.NewHTTPClient(srv.URL, "gw-test",
		map[string]string{"mqtt": "mqtt-01"}, provisioning.TLSOptions{})
	require.NoError(t, err)

	filePath := filepath.Join(t.TempDir(), "pl.csv")
	require.NoError(t, os.WriteFile(filePath,
		[]byte("point_id,local_id,protocol\nfile_point,sensors/file,mqtt\n"), 0o600))
	fileClient := provisioning.NewFileClient(filePath, "mqtt-01", nil)

	fb := provisioning.NewFallbackClient(httpClient, fileClient)

	resolver := pointlist.NewSynced(nil)
	loop := pointsync.New(fb, resolver, pointsync.Config{Interval: 30 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)

	require.Eventually(t, func() bool {
		_, ok := resolver.Resolve("mqtt-01", "sensors/file")
		return ok
	}, time.Second, 5*time.Millisecond, "must resolve from the local file while Building OS is unreachable")

	bosUp.Store(true) // Building OS comes up.

	require.Eventually(t, func() bool {
		_, ok := resolver.Resolve("mqtt-01", "sensors/bos")
		return ok
	}, 2*time.Second, 10*time.Millisecond,
		"must converge to Building OS's point list once reachable, without a gateway restart")

	_, stillFromFile := resolver.Resolve("mqtt-01", "sensors/file")
	assert.False(t, stillFromFile,
		"the file-sourced point must be replaced by Building OS's full resnapshot on promotion")
}
