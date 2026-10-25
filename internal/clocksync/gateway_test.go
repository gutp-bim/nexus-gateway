// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package clocksync

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	pb "nexus-gateway/gen"
	"nexus-gateway/internal/catalog"
	"nexus-gateway/internal/dispatch"
	"nexus-gateway/internal/lifecycle"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
	"nexus-gateway/internal/storeforward"
	"nexus-gateway/internal/testca"
)

// Only the Engine boundary is fake. Gateway/controller, NATS request/reply,
// JetStream durable consumer/ACKs, archive, Normalizer and SQLite are real.
type daemon struct {
	lifecycle.ContainerClient
	mu          sync.Mutex
	nc          *nats.Conn
	config      *container.Config
	id          string
	running     bool
	sub         *nats.Subscription
	badReceipt  bool
	badReceipts int
	failStart   int
	starts      int
	python      string
	script      string
	stderr      *os.File
	process     *exec.Cmd
	exited      chan struct{}
}

func (d *daemon) ContainerCreate(ctx context.Context, c *container.Config, h *container.HostConfig, n *dockernetwork.NetworkingConfig, p *ocispec.Platform, name string) (container.CreateResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.id != "" {
		return container.CreateResponse{}, errors.New("existing container")
	}
	d.config = c
	d.id = fmt.Sprintf("fake-%d", d.starts+1)
	return container.CreateResponse{ID: d.id}, nil
}
func (d *daemon) ContainerStart(ctx context.Context, id string, _ container.StartOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = true
	d.starts++
	if d.python != "" {
		cmd := exec.Command(d.python, d.script)
		root := filepath.Dir(filepath.Dir(d.script))
		cmd.Env = append(append(os.Environ(), d.config.Env...), "PYTHONPATH="+root, "PYTHONDONTWRITEBYTECODE=1")
		cmd.Stderr = d.stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		d.process = cmd
		d.exited = make(chan struct{})
		exited := d.exited
		go func() { _ = cmd.Wait(); close(exited) }()
		return nil
	}
	env := map[string]string{}
	for _, e := range d.config.Env {
		k, v, _ := strings.Cut(e, "=")
		env[k] = v
	}
	raw, err := os.ReadFile(env["BACNET_CLOCK_CONFIG_FILE"])
	if err != nil {
		return err
	}
	var native struct {
		Revision string `json:"revision"`
	}
	if err = json.Unmarshal(raw, &native); err != nil {
		return err
	}
	session, revision, hash := env["BACNET_MANAGED_SESSION"], native.Revision, fmt.Sprintf("%x", sha256.Sum256(raw))
	paused := true
	d.sub, err = d.nc.Subscribe("cfg.bacnet."+scope.ConnectorID+".runtime", func(msg *nats.Msg) {
		d.mu.Lock()
		defer d.mu.Unlock()
		var req map[string]string
		_ = json.Unmarshal(msg.Data, &req)
		if req["session"] != session {
			_ = msg.Respond([]byte(`{"error":"session mismatch"}`))
			return
		}
		switch req["action"] {
		case "quiesce":
			paused = true
		case "resume":
			paused = false
		}
		receipt := runtimeReceipt{Capability: "clock-runtime/1", Session: session, Revision: revision, Hash: hash, Paused: &paused}
		if (d.badReceipt || d.badReceipts > 0) && req["action"] == "status" {
			d.badReceipt = false
			if d.badReceipts > 0 {
				d.badReceipts--
			}
			receipt.Hash = "wrong"
		}
		body, _ := json.Marshal(receipt)
		_ = msg.Respond(body)
	})
	if err != nil {
		return err
	}
	if err = d.nc.FlushWithContext(ctx); err != nil {
		return err
	}
	if d.failStart > 0 {
		d.failStart--
		return errors.New("injected partial start")
	}
	return nil
}
func (d *daemon) ContainerStop(ctx context.Context, id string, _ container.StopOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = false
	if d.process != nil {
		_ = d.process.Process.Signal(os.Interrupt)
		select {
		case <-d.exited:
		case <-ctx.Done():
			_ = d.process.Process.Kill()
			<-d.exited
			return ctx.Err()
		}
		d.process = nil
	}
	if d.sub != nil {
		_ = d.sub.Unsubscribe()
		d.sub = nil
	}
	return d.nc.FlushWithContext(ctx)
}
func (d *daemon) ContainerRemove(ctx context.Context, id string, _ container.RemoveOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return errors.New("still running")
	}
	d.id = ""
	return nil
}
func (d *daemon) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.id == "" {
		return container.InspectResponse{}, errdefs.NotFound(errors.New("not found"))
	}
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: d.id, State: &container.State{Running: d.running}}, Config: d.config}, nil
}

func liveFixture(t *testing.T) (*Gateway, *Controller, *daemon, *source, jetstream.JetStream, *lifecycle.Manager, *pointlist.SyncedResolver, context.CancelFunc) {
	return liveFixtureEngine(t, false)
}

func liveFixtureEngine(t *testing.T, python bool) (*Gateway, *Controller, *daemon, *source, jetstream.JetStream, *lifecycle.Manager, *pointlist.SyncedResolver, context.CancelFunc) {
	t.Helper()
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second))
	t.Cleanup(ns.Shutdown)
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateStream(t.Context(), jetstream.StreamConfig{Name: "EVENTS", Subjects: []string{"evt.>"}})
	require.NoError(t, err)
	d := &daemon{nc: nc}
	if python {
		root, err := filepath.Abs("../..")
		require.NoError(t, err)
		d.python = filepath.Join(root, "connector/bacnet/.venv/bin/python")
		if explicit := os.Getenv("NEXUS_CLOCK_PYTHON"); explicit != "" {
			d.python = explicit
		} else if _, err = os.Stat(d.python); os.IsNotExist(err) {
			t.Skip("BACnet venv absent; set NEXUS_CLOCK_PYTHON to require subprocess verification")
		}
		d.script = filepath.Join(root, "connector/bacnet/tests/managed_process.py")
		d.stderr, err = os.Create(filepath.Join(t.TempDir(), "python.log"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.stderr.Close() })
	}
	registry := lifecycle.NewRegistry()
	manager := lifecycle.NewManagerWithConfig(d, registry, lifecycle.ManagerConfig{NATSURL: ns.ClientURL()})
	resolver := pointlist.NewSynced(nil)
	buffer, err := storeforward.Open(filepath.Join(t.TempDir(), "sf.db"), 1000)
	require.NoError(t, err)
	t.Cleanup(func() { _ = buffer.Close() })
	spec := lifecycle.ConnectorSpec{ID: scope.ConnectorID, Image: "example.com/bacnet@sha256:" + strings.Repeat("a", 64), Env: []string{"BACNET_ADDRESS=192.0.2.1"}}
	g, err := NewGateway(t.Context(), nc, js, manager, spec, t.TempDir(), scope, resolver, buffer, nil)
	require.NoError(t, err)
	old, _ := snapshots(t)
	src := &source{s: old}
	c, err := New(src, g.Runtime(), scope, 2*time.Second)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	t.Cleanup(func() {
		stop, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		g.RequestStop()
		_ = g.Shutdown(stop)
		cancel()
		<-done
	})
	return g, c, d, src, js, manager, resolver, cancel
}

func TestGatewayActualPythonProducerAndRecoveryJournal(t *testing.T) {
	g, c, _, src, _, _, resolver, _ := liveFixtureEngine(t, true)
	// The same production client used by cmd/gateway fetches the immutable real
	// Builder→RI snapshot over a real mTLS mock, with no injected identity header.
	var current atomic.Pointer[provisioning.ClockSnapshot]
	current.Store(src.s)
	ca := testca.New(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/gateways/"+scope.GatewayID+"/provisioning/v1", r.URL.Path)
		require.Empty(t, r.Header.Get("X-Gateway-Id"))
		require.Empty(t, r.Header.Get("Authorization"))
		require.NotEmpty(t, r.TLS.VerifiedChains)
		snapshot := current.Load()
		w.Header().Set("ETag", snapshot.Revision())
		if r.Header.Get("If-None-Match") == snapshot.Revision() {
			w.WriteHeader(304)
			return
		}
		_, _ = w.Write(snapshot.Bytes())
	}))
	srv.TLS = ca.IssueServer(t, "localhost").ServerTLSConfig(t)
	srv.TLS.ClientCAs = ca.Pool()
	srv.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	srv.StartTLS()
	defer srv.Close()
	dir := t.TempDir()
	cert, key := ca.IssueClient(t, scope.GatewayID).WriteFiles(t, dir, "gateway")
	client, err := provisioning.NewClockHTTPClient(srv.URL, scope, provisioning.ClockHTTPOptions{TLS: provisioning.TLSOptions{CAFile: testca.WritePEM(t, dir, "ca.pem", ca.CertPEM()), CertFile: cert, KeyFile: key, ServerName: "localhost"}})
	require.NoError(t, err)
	c.source = client
	require.NoError(t, c.Sync(t.Context()))
	first := g.producer.record
	stat, err := os.Stat(filepath.Join(g.store.dir, "native", first.Session+".json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0444), stat.Mode().Perm(), "unprivileged connector can read its readonly native bundle")
	require.Eventually(t, func() bool { return g.buffer.Written() >= 35 }, 3*time.Second, 10*time.Millisecond)
	_, src.s = snapshots(t)
	current.Store(src.s)
	require.NoError(t, c.Sync(t.Context()))
	second := g.producer.record
	require.Eventually(t, func() bool { return g.buffer.Written() >= 70 }, 3*time.Second, 10*time.Millisecond)
	batch, err := g.buffer.ReadBatch(0, 1000)
	require.NoError(t, err)
	for i, frame := range batch {
		revision := first.snapshot.Revision()
		prefix := "BPT-"
		if i >= 35 {
			revision = second.snapshot.Revision()
			prefix = "NEW-BPT-"
		}
		require.True(t, strings.HasPrefix(frame.Frame.PointId, prefix))
		require.Equal(t, revision, frame.Frame.Attributes["sim_config_revision"])
		require.Equal(t, "absent", frame.Frame.Attributes["sim_run_status"])
		require.Contains(t, frame.Frame.Timestamp, ".")
	}
	// Recovery restores a rollback candidate only, with no fabricated applied ACK.
	last, err := g.LastGood()
	require.NoError(t, err)
	require.Equal(t, src.s.Revision(), last.Revision())
	newController, err := New(client, g.Runtime(), scope, 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, newController.RestoreLastGood(last))
	require.Empty(t, newController.AppliedRevision())
	require.NoError(t, newController.Sync(t.Context()))
	require.Equal(t, src.s.Revision(), newController.AppliedRevision())
	require.Equal(t, src.s.Entries(), resolver.Snapshot())
}

func TestGatewayHoldWaitsForInflightCommand(t *testing.T) {
	g, c, _, _, _, _, resolver, _ := liveFixture(t)
	require.NoError(t, c.Sync(t.Context()))
	entered, release := make(chan struct{}), make(chan struct{})
	sub, err := g.nc.Subscribe("cmd.bacnet."+scope.ConnectorID, func(msg *nats.Msg) {
		close(entered)
		<-release
		_ = msg.Respond([]byte(`{"success":true,"response":"ok"}`))
	})
	require.NoError(t, err)
	defer sub.Unsubscribe()
	require.NoError(t, g.nc.Flush())
	d := dispatch.New(g.nc, resolver, 2*time.Second).WithAdmission(g)
	finished := make(chan *pb.ControlResult, 1)
	go func() {
		finished <- d.Execute(t.Context(), &pb.ControlCommand{ControlId: "active", PointId: "BPT-0033", PresentValue: 0.5})
	}()
	<-entered
	held := make(chan error, 1)
	go func() { held <- g.Hold(t.Context()) }()
	require.Eventually(t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.held }, time.Second, time.Millisecond)
	require.Equal(t, "configuration_paused", d.Execute(t.Context(), &pb.ControlCommand{ControlId: "new", PointId: "BPT-0033"}).Response)
	select {
	case <-held:
		t.Fatal("hold returned before the old command reply")
	default:
	}
	close(release)
	require.True(t, (<-finished).Success)
	require.NoError(t, <-held)
}

func publishSession(t *testing.T, js jetstream.JetStream, record *sessionRecord, session string) {
	t.Helper()
	cfg := record.snapshot.Native()
	attrs := map[string]string{}
	if len(cfg.Clocks) > 0 {
		attrs = map[string]string{"sim_clock_id": cfg.Clocks[0].ClockID, "sim_config_revision": cfg.Revision, "sim_run_status": "absent", "sim_time_quality": "stable_unfenced", "sim_time_s": "120.5"}
	}
	body, _ := json.Marshal(map[string]any{"protocol": "bacnet", "connector_id": scope.ConnectorID, "local_id": cfg.Points[0].LocalID, "device_ref": cfg.Points[0].DeviceRef, "value": 21.5, "quality": "Good", "timestamp": "2026-10-25T01:02:03.125000Z", "attributes": attrs})
	_, err := js.PublishMsg(t.Context(), &nats.Msg{Subject: "evt.bacnet." + scope.ConnectorID, Header: nats.Header{"Nexus-Session": []string{session}, "Nexus-Config-Revision": []string{cfg.Revision}}, Data: body})
	require.NoError(t, err)
}

func TestGatewayAppliesAndReplaysArchivedSessions(t *testing.T) {
	g, c, _, src, js, manager, resolver, _ := liveFixture(t)
	require.NoError(t, c.Sync(t.Context()))
	oldSession := g.producer.record
	publishSession(t, js, oldSession, oldSession.Session)
	require.Eventually(t, func() bool { return g.buffer.Written() == 1 }, time.Second, 10*time.Millisecond)
	_, newer := snapshots(t)
	src.s = newer
	require.NoError(t, c.Sync(t.Context()))
	newSession := g.producer.record
	publishSession(t, js, oldSession, oldSession.Session) // delayed old publish AFTER the new mapping is active
	publishSession(t, js, newSession, newSession.Session)
	require.Eventually(t, func() bool { return g.buffer.Written() == 3 }, time.Second, 10*time.Millisecond)
	batch, err := g.buffer.ReadBatch(0, 100)
	require.NoError(t, err)
	require.Len(t, batch, 3)
	require.Equal(t, "BPT-0002", batch[1].Frame.PointId)
	require.Equal(t, oldSession.snapshot.Revision(), batch[1].Frame.Attributes["sim_config_revision"])
	require.Equal(t, "NEW-BPT-0002", batch[2].Frame.PointId)
	require.Equal(t, newSession.snapshot.Revision(), batch[2].Frame.Attributes["sim_config_revision"])
	require.Equal(t, batch[0].Frame.Timestamp, batch[1].Frame.Timestamp)
	require.Equal(t, batch[0].Frame.Value, batch[1].Frame.Value)
	// A broker redelivery/republish of the old raw message cannot see resolver's
	// deliberately changed current point IDs, including after archive reload.
	store, err := openSessions(g.store.dir, scope)
	require.NoError(t, err)
	recovered, ok := store.get(oldSession.Session)
	require.True(t, ok)
	require.True(t, recovered.verified)
	publishSession(t, js, newSession, "unknown")
	require.Eventually(t, func() bool {
		files, _ := filepath.Glob(filepath.Join(g.store.dir, "quarantine", "*.json"))
		return len(files) == 1
	}, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 3, g.buffer.Written())
	for _, action := range []func(context.Context, string) error{manager.Start, manager.Stop, manager.Restart, manager.Rollback} {
		require.ErrorIs(t, action(t.Context(), scope.ConnectorID), lifecycle.ErrManaged)
	}
	require.ErrorIs(t, manager.Upgrade(t.Context(), scope.ConnectorID, "other"), lifecycle.ErrManaged)
	manifest := catalog.Manifest{Name: scope.ConnectorID, Image: "example.com/bacnet", Digest: "sha256:" + strings.Repeat("b", 64)}
	require.ErrorIs(t, manager.Update(t.Context(), scope.ConnectorID, manifest, catalog.NoopVerifier{}, []string{"example.com"}, "1.0.0", time.Second), lifecycle.ErrManaged)
	require.ErrorIs(t, manager.Install(t.Context(), manifest, catalog.NoopVerifier{}, []string{"example.com"}, "1.0.0"), lifecycle.ErrManaged)
	dispatcher := dispatch.New(g.nc, resolver, time.Second).WithAdmission(g)
	require.NoError(t, g.Hold(t.Context()))
	result := dispatcher.Execute(t.Context(), &pb.ControlCommand{ControlId: "held", PointId: "NEW-BPT-0033", PresentValue: 1})
	require.Equal(t, "configuration_paused", result.Response)
}

func TestGatewayWrongReceiptAndPartialStartRollback(t *testing.T) {
	for _, failure := range []string{"receipt", "partial-start"} {
		t.Run(failure, func(t *testing.T) {
			g, c, d, src, _, _, resolver, _ := liveFixture(t)
			require.NoError(t, c.Sync(t.Context()))
			old := src.s
			_, src.s = snapshots(t)
			d.mu.Lock()
			if failure == "receipt" {
				d.badReceipt = true
			} else {
				d.failStart = 1
			}
			d.mu.Unlock()
			require.Error(t, c.Sync(t.Context()))
			require.Equal(t, old.Revision(), c.AppliedRevision())
			require.Equal(t, old.Entries(), resolver.Snapshot())
			good, err := g.LastGood()
			require.NoError(t, err)
			require.Equal(t, old.Revision(), good.Revision())
			require.NoError(t, c.Sync(t.Context()))
			require.Equal(t, src.s.Revision(), c.AppliedRevision())
		})
	}
}

func TestGatewayFailedRestorationStopsAndHolds(t *testing.T) {
	g, c, d, src, js, _, _, _ := liveFixture(t)
	require.NoError(t, c.Sync(t.Context()))
	oldSession := g.producer.record
	_, src.s = snapshots(t)
	d.mu.Lock()
	d.badReceipts = 2
	d.mu.Unlock()
	require.Error(t, c.Sync(t.Context()))
	require.Equal(t, "halted", c.Status().Phase)
	require.Empty(t, c.AppliedRevision())
	d.mu.Lock()
	running := d.running
	d.mu.Unlock()
	require.False(t, running)
	_, admitted := g.Enter(t.Context())
	require.False(t, admitted)
	// Delayed old data stays held, then resolves against its original archive
	// after recovery. Unverified candidate data is durably quarantined.
	publishSession(t, js, oldSession, oldSession.Session)
	g.store.mu.RLock()
	var unverified *sessionRecord
	for _, r := range g.store.records {
		if !r.verified {
			copy := *r
			unverified = &copy
			break
		}
	}
	g.store.mu.RUnlock()
	require.NotNil(t, unverified)
	publishSession(t, js, unverified, unverified.Session)
	require.NoError(t, c.Sync(t.Context()))
	require.Eventually(t, func() bool { return g.buffer.Written() == 1 }, time.Second, 10*time.Millisecond)
	batch, err := g.buffer.ReadBatch(0, 100)
	require.NoError(t, err)
	require.Equal(t, "BPT-0002", batch[0].Frame.PointId)
	require.Eventually(t, func() bool {
		files, _ := filepath.Glob(filepath.Join(g.store.dir, "quarantine", "*.json"))
		return len(files) == 1
	}, time.Second, 10*time.Millisecond)
}
