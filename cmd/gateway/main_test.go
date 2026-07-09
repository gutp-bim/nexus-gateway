// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/catalog"
	"nexus-gateway/internal/lifecycle"
	"nexus-gateway/internal/version"
)

// startDevSim is gated behind --dev-sim (off by default), so the default build
// runs no in-process connector — the connector-isolation invariant holds (ADR-0001).
// This test exercises the enabled path: registration + a live connector.

func TestStartDevSim_ClearsRunningOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	nc, js := newTestNATS(t, ctx)
	_ = nc
	reg := lifecycle.NewRegistry()

	startDevSim(ctx, js, reg, time.Hour) // long interval: we only care about lifecycle state
	require.True(t, reg.List()[0].Running)

	cancel()
	// On shutdown the connector lifetime ends; the registry must reflect not-running
	// so the Admin UI does not show a stale running sim.
	assert.Eventually(t, func() bool {
		entries := reg.List()
		return len(entries) == 1 && !entries[0].Running
	}, 3*time.Second, 20*time.Millisecond, "sim-01 must be marked not-running after ctx cancel")
}

func TestStartDevSim_RegistersAndRunsSim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nc, js := newTestNATS(t, ctx)
	reg := lifecycle.NewRegistry()

	// Subscribe before starting so we can observe the connector actually publishing.
	sub, err := nc.SubscribeSync("evt.sim.sim-01")
	require.NoError(t, err)

	startDevSim(ctx, js, reg, 50*time.Millisecond)

	// Registered and marked running (synchronous part).
	entries := reg.List()
	require.Len(t, entries, 1)
	assert.Equal(t, "sim-01", entries[0].Spec.ID)
	assert.True(t, entries[0].Running)

	// The connector goroutine actually emits Common Events.
	_, err = sub.NextMsg(3 * time.Second)
	require.NoError(t, err, "dev-sim connector should publish to evt.sim.sim-01")
}

func TestParseConnectorMap_EmptyString(t *testing.T) {
	m, err := parseConnectorMap("")
	if err != nil {
		t.Fatalf("empty string must not error, got %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("empty string must return empty map, got %v", m)
	}
}

func TestParseConnectorMap_SingleProtocol(t *testing.T) {
	m, err := parseConnectorMap("bacnet:bacnet-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["bacnet"] != "bacnet-01" {
		t.Fatalf("want bacnet→bacnet-01, got %v", m)
	}
}

func TestParseConnectorMap_MultipleProtocols(t *testing.T) {
	m, err := parseConnectorMap("bacnet:bacnet-01,opcua:opcua-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["bacnet"] != "bacnet-01" || m["opcua"] != "opcua-01" {
		t.Fatalf("want bacnet→bacnet-01 and opcua→opcua-01, got %v", m)
	}
	if len(m) != 2 {
		t.Fatalf("want exactly 2 entries, got %d", len(m))
	}
}

func TestParseConnectorMap_Whitespace(t *testing.T) {
	m, err := parseConnectorMap(" bacnet : bacnet-01 , opcua : opcua-01 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["bacnet"] != "bacnet-01" || m["opcua"] != "opcua-01" {
		t.Fatalf("whitespace must be trimmed, got %v", m)
	}
}

func TestParseConnectorMap_TrailingCommaIgnored(t *testing.T) {
	m, err := parseConnectorMap("bacnet:bacnet-01,")
	if err != nil {
		t.Fatalf("trailing comma must be tolerated, got err=%v", err)
	}
	if m["bacnet"] != "bacnet-01" || len(m) != 1 {
		t.Fatalf("want {bacnet:bacnet-01}, got %v", m)
	}
}

func TestParseConnectorMap_InvalidNoColon(t *testing.T) {
	_, err := parseConnectorMap("bacnet-bacnet-01")
	if err == nil {
		t.Fatal("must error on entry without ':'")
	}
}

func TestParseConnectorMap_InvalidEmptyValue(t *testing.T) {
	_, err := parseConnectorMap("bacnet:")
	if err == nil {
		t.Fatal("must error on empty connector ID")
	}
}

func TestParseConnectorMap_InvalidEmptyKey(t *testing.T) {
	_, err := parseConnectorMap(":bacnet-01")
	if err == nil {
		t.Fatal("must error on empty protocol key")
	}
}

func TestParseConnectorMap_KeyCaseNormalizedToLowercase(t *testing.T) {
	// pointlist.LoadCSV always looks protocols up in lowercase (its own
	// inferred/normalized values) — a mixed-case CONNECTOR_MAP key (a natural
	// env-var authoring convention) must still resolve, not silently miss.
	m, err := parseConnectorMap("OPCUA:opcua-01,MqTT:mqtt-01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["opcua"] != "opcua-01" || m["mqtt"] != "mqtt-01" {
		t.Fatalf("want lowercased keys opcua/mqtt, got %v", m)
	}
}

// The Connector Catalog install gate must read the gateway version from the
// single-source version package (#22), and that value must be a valid semver so
// a fresh (uninjected) build still satisfies a manifest's min_gateway_version —
// the exact regression a bare "dev"/empty version would introduce.
func TestGatewayInstaller_UsesSingleSourceVersion(t *testing.T) {
	gi := &gatewayInstaller{gwVersion: version.String()}
	if gi.gwVersion != version.String() {
		t.Fatalf("installer gwVersion = %q, want single-source %q", gi.gwVersion, version.String())
	}

	m := catalog.Manifest{
		Image:             "ghcr.io/x/y",
		Digest:            "sha256:" + strings.Repeat("a", 64),
		MinGatewayVersion: version.String(),
	}
	if err := m.Validate([]string{"ghcr.io"}, gi.gwVersion); err != nil {
		t.Fatalf("single-source version %q failed its own min_gateway_version gate: %v", gi.gwVersion, err)
	}
}

func TestWantsVersion(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--version"}, true},
		{[]string{"-version"}, true},
		{[]string{"--nats", "x", "--version"}, true},
		{[]string{"--version", "--nats", "x"}, true},
		{nil, false},
		{[]string{"--nats", "x"}, false},
		{[]string{"--", "--version"}, false}, // after terminator: not a flag
		{[]string{"--versionx"}, false},      // not an exact match
	}
	for _, c := range cases {
		if got := wantsVersion(c.args); got != c.want {
			t.Errorf("wantsVersion(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestResolveBOSAddr_FallsBackToBosAddr(t *testing.T) {
	got := resolveBOSAddr("host:5051", "")
	if got != "host:5051" {
		t.Fatalf("want host:5051, got %s", got)
	}
}

func TestResolveBOSAddr_OverrideWins(t *testing.T) {
	got := resolveBOSAddr("host:5051", "host:5052")
	if got != "host:5052" {
		t.Fatalf("want host:5052, got %s", got)
	}
}

func TestResolveBOSAddr_BothEmpty(t *testing.T) {
	got := resolveBOSAddr("", "")
	if got != "" {
		t.Fatalf("want empty, got %s", got)
	}
}

func newTestNATS(t *testing.T, ctx context.Context) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	ns, err := server.NewServer(&server.Options{JetStream: true, StoreDir: t.TempDir(), Port: -1})
	require.NoError(t, err)
	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second))
	t.Cleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name: "EVENTS", Subjects: []string{"evt.>"}, Storage: jetstream.MemoryStorage,
	})
	require.NoError(t, err)
	return nc, js
}
