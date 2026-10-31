// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	pb "nexus-gateway/gen"
	"nexus-gateway/internal/dispatch"
	"nexus-gateway/internal/egress"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
)

// This covers the northbound provisioning/gRPC contracts through the production
// Python handler and BACpypes commandable object. HTTP/gRPC BOS servers are fakes;
// use TestE2E_BosReleaseDispatch for the actual RI API/Bridge after RI #600 lands.
func TestControl_BACnetRelease(t *testing.T) {
	python := os.Getenv("E2E_BACNET_PYTHON")
	if python == "" {
		t.Skip("set E2E_BACNET_PYTHON to a Python with connector/bacnet installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
	scanner := bufio.NewScanner(stdout)
	require.True(t, scanner.Scan(), "connector did not start")
	require.Equal(t, "ready", scanner.Text())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/gateways/gw-release/pointlist", r.URL.Path)
		fmt.Fprint(w, `{"full":true,"points":[{"pointId":"target","native":{"protocol":"bacnet","objectType":"analogOutput","instanceNo":"37","deviceId":"device-test"},"writable":true},{"pointId":"release","releaseOf":"target","writable":true}]}`)
	}))
	defer srv.Close()
	client, err := provisioning.NewHTTPClient(srv.URL, "gw-release", map[string]string{"bacnet": "bacnet-release"}, provisioning.TLSOptions{})
	require.NoError(t, err)
	snapshot, err := client.Fetch(ctx, "")
	require.NoError(t, err)
	resolver := pointlist.NewSynced(nil)
	resolver.Update(snapshot.Entries)
	d := dispatch.New(nc, resolver, 2*time.Second)
	results := make(chan *pb.ControlResult, 10)
	bos := startMockEgress(t, results)
	agentCtx, stopAgent := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); egress.New(bos.addr, "gw-release", d, insecureCreds(), nil).Run(agentCtx) }()
	defer func() { stopAgent(); <-done }()
	send := func(id, point string, value float64, priority int32, success bool) {
		t.Helper()
		bos.SendCommand(&pb.ControlCommand{ControlId: id, PointId: point, PresentValue: value, Priority: priority})
		select {
		case result := <-results:
			require.Equal(t, id, result.ControlId)
			require.Equal(t, success, result.Success, result.Response)
		case <-ctx.Done():
			t.Fatal("result deadline exceeded")
		}
	}
	type state struct {
		Value  float64
		Slots  []*float64
		Writes int
	}
	read := func() state {
		msg, err := nc.RequestWithContext(ctx, "test.bacnet.state", nil)
		require.NoError(t, err)
		var s state
		require.NoError(t, json.Unmarshal(msg.Data, &s))
		return s
	}
	send("write", "target", 22, 0, true)
	require.Equal(t, 22.0, read().Value)
	send("release", "release", 1, 0, true)
	s := read()
	require.Equal(t, 18.0, s.Value)
	require.Nil(t, s.Slots[7])
	require.Equal(t, 2, s.Writes)
	send("release", "release", 1, 0, true)
	require.Equal(t, 2, read().Writes)
	send("other-priority", "target", 19, 10, true)
	send("override", "target", 23, 4, true)
	send("release-priority", "release", 1, 4, true)
	s = read()
	require.Equal(t, 19.0, s.Value)
	require.Nil(t, s.Slots[3])
	require.Equal(t, 19.0, *s.Slots[9])
	send("invalid-zero", "release", 0, 0, false)
	require.Equal(t, 5, read().Writes)
}
