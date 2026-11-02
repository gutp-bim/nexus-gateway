// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointsync_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/pointsync"
	"nexus-gateway/internal/provisioning"
)

func TestReleaseRelationsFollowPointListDelta(t *testing.T) {
	var revision atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch revision.Load() {
		case 0:
			fmt.Fprint(w, `{"revision":"v0","full":true,"points":[{"pointId":"a","localId":"analogOutput,1"},{"pointId":"b","localId":"analogOutput,2"},{"pointId":"r1","releaseOf":"a"},{"pointId":"r2","releaseOf":"a"}]}`)
		case 1:
			fmt.Fprint(w, `{"revision":"v1","full":false,"changed":[{"pointId":"r1","releaseOf":"b"}]}`)
		case 2:
			fmt.Fprint(w, `{"revision":"v2","full":false,"removed":["b","r2"]}`)
		}
	}))
	defer srv.Close()
	resolver := pointlist.NewSynced(nil)
	pushes := make(chan struct{}, 1)
	client, err := provisioning.NewHTTPClient(srv.URL, "gw", nil, provisioning.TLSOptions{})
	require.NoError(t, err)
	loop := pointsync.New(client, resolver, pointsync.Config{Interval: time.Hour}).WithRevalidate(pushes)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); loop.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-loop.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("initial sync timeout")
	}
	entry, target, ok := resolver.ResolveControl("r1")
	require.True(t, ok)
	require.Equal(t, "a", entry.ReleaseOf)
	require.Equal(t, "a", target.PointID)
	revision.Store(1)
	pushes <- struct{}{}
	require.Eventually(t, func() bool { e, _, _ := resolver.ResolveControl("r1"); return e.ReleaseOf == "b" }, 2*time.Second, time.Millisecond)
	_, _, ok = resolver.ResolveControl("r2")
	require.True(t, ok, "virtual points sharing empty native IDs must not evict each other")
	revision.Store(2)
	pushes <- struct{}{}
	require.Eventually(t, func() bool { _, _, exists := resolver.ResolveControl("r2"); return !exists }, 2*time.Second, time.Millisecond)
	_, target, ok = resolver.ResolveControl("r1")
	require.True(t, ok)
	require.Empty(t, target.PointID, "deleted target must never remain routable")
}
