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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/common"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
	"nexus-gateway/internal/storeforward"
	"nexus-gateway/internal/testca"
)

var scope = provisioning.ClockScope{GatewayID: "GW-BOPTEST-001", ConnectorID: "bacnet-boptest", DeviceID: 599}

func snapshots(t *testing.T) (*provisioning.ClockSnapshot, *provisioning.ClockSnapshot) {
	t.Helper()
	data, err := os.ReadFile("../../test/contracts/builder-ri-9083af2/provisioning.json")
	require.NoError(t, err)
	old, err := provisioning.StageClock(data, scope)
	require.NoError(t, err)
	var b map[string]any
	require.NoError(t, json.Unmarshal(data, &b))
	for _, p := range b["points"].([]any) {
		p.(map[string]any)["pointId"] = "NEW-" + p.(map[string]any)["pointId"].(string)
	}
	b["clockSources"].([]any)[0].(map[string]any)["native"].(map[string]any)["objectInstance"] = 87
	delete(b, "revision")
	delete(b, "generatedAt")
	data, err = json.Marshal(b)
	require.NoError(t, err)
	b["revision"] = fmt.Sprintf("\"sha256:%x\"", sha256.Sum256(data))
	data, err = json.Marshal(b)
	require.NoError(t, err)
	newer, err := provisioning.StageClock(data, scope)
	require.NoError(t, err)
	return old, newer
}

type source struct {
	s     *provisioning.ClockSnapshot
	err   error
	known []string
}

func (s *source) Fetch(ctx context.Context, known string) (*provisioning.ClockSnapshot, error) {
	s.known = append(s.known, known)
	if s.err != nil {
		return nil, s.err
	}
	if s.s != nil && s.s.Revision() == known {
		return nil, nil
	}
	return s.s, nil
}

// One fake owns the process/gate lease, but storage, normalization, mapping
// swaps, coordinator and HTTP client are the real implementations.
type process struct {
	t             *testing.T
	resolver      *pointlist.SyncedResolver
	buffer        *storeforward.Buffer
	cfg           provisioning.NativeClockConfig
	held, running bool
	queue         [][]byte
	log           []string
	fail          map[string]int
	wrongRevision bool
	check         func(string)
}

func (p *process) op(name string) error {
	p.log = append(p.log, name)
	if p.check != nil {
		p.check(name)
	}
	if p.fail[name] > 0 {
		p.fail[name]--
		return fmt.Errorf("injected %s", name)
	}
	return nil
}
func (p *process) Hold(ctx context.Context) error { p.held = true; return p.op("hold") }
func (p *process) Stop(ctx context.Context) error {
	require.True(p.t, p.held)
	if err := p.op("stop"); err != nil {
		return err
	}
	p.running = false
	return nil
}
func (p *process) Drain(ctx context.Context) error {
	require.True(p.t, p.held)
	require.False(p.t, p.running)
	if err := p.op("drain"); err != nil {
		return err
	}
	return p.save()
}
func (p *process) Configure(ctx context.Context, n provisioning.NativeClockConfig) error {
	require.True(p.t, p.held)
	require.False(p.t, p.running)
	if err := p.op("configure"); err != nil {
		return err
	}
	p.cfg = n
	return nil
}
func (p *process) Start(ctx context.Context) error {
	require.True(p.t, p.held)
	p.running = true
	p.enqueue()
	return p.op("start")
}
func (p *process) Revision(ctx context.Context) (string, error) {
	require.True(p.t, p.running)
	if err := p.op("verify"); err != nil {
		return "", err
	}
	if p.wrongRevision {
		p.wrongRevision = false
		return "wrong", nil
	}
	return p.cfg.Revision, nil
}
func (p *process) Resume(ctx context.Context) error {
	require.True(p.t, p.held)
	require.True(p.t, p.running)
	if err := p.op("resume"); err != nil {
		return err
	}
	if err := p.save(); err != nil {
		return err
	}
	p.held = false
	return nil
}
func (p *process) enqueue() {
	if len(p.cfg.Points) == 0 {
		return
	}
	e := common.Event{Protocol: "bacnet", ConnectorID: scope.ConnectorID, LocalID: p.cfg.Points[0].LocalID, DeviceRef: "599", Value: common.NumberValue(12.5), Timestamp: "2026-10-25T00:00:00.125000Z", Quality: "Good"}
	if len(p.cfg.Clocks) > 0 {
		e.Attributes = map[string]string{"sim_clock_id": p.cfg.Clocks[0].ClockID, "sim_config_revision": p.cfg.Revision, "sim_time_quality": "stable_unfenced", "sim_time_s": "120.5", "sim_run_status": "absent"}
	}
	b, err := json.Marshal(e)
	require.NoError(p.t, err)
	p.queue = append(p.queue, b)
}
func (p *process) save() error {
	for len(p.queue) > 0 {
		frame, out := normalizer.Normalize(p.queue[0], p.resolver, scope.GatewayID)
		require.Equal(p.t, normalizer.OutcomeOK, out)
		if err := p.buffer.Write(frame); err != nil {
			return err
		}
		p.queue = p.queue[1:]
	}
	return nil
}

func setup(t *testing.T, s Source) (*Controller, *process) {
	t.Helper()
	resolver := pointlist.NewSynced(nil)
	buffer, err := storeforward.Open(filepath.Join(t.TempDir(), "apply.db"), 100)
	require.NoError(t, err)
	t.Cleanup(func() { _ = buffer.Close() })
	p := &process{t: t, resolver: resolver, buffer: buffer, fail: map[string]int{}}
	runtime, err := NewResolverRuntime(p, p, resolver)
	require.NoError(t, err)
	c, err := New(s, runtime, scope, time.Second)
	require.NoError(t, err)
	return c, p
}

func TestApplyDrainsBeforeMappingChangeAndRevalidatesRuntime(t *testing.T) {
	old, newer := snapshots(t)
	src := &source{s: old}
	c, p := setup(t, src)
	require.NoError(t, c.Sync(t.Context()))
	require.Equal(t, old.Revision(), c.AppliedRevision())
	require.False(t, p.held)
	p.enqueue() // queued old observation: it must resolve under the OLD mapping
	p.log = nil
	src.s = newer
	p.check = func(op string) {
		if op == "start" || op == "verify" || op == "resume" {
			require.Empty(t, c.AppliedRevision(), "no runtime applied claim before verification/resume")
		}
	}
	require.NoError(t, c.Sync(t.Context()))
	require.Equal(t, []string{"hold", "stop", "drain", "configure", "start", "verify", "resume"}, p.log)
	require.Equal(t, newer.Revision(), c.Status().DesiredRevision)
	require.Equal(t, newer.Revision(), c.Status().LastGoodRevision)
	batch, err := p.buffer.ReadBatch(0, 100)
	require.NoError(t, err)
	require.Len(t, batch, 3)
	require.Equal(t, "BPT-0002", batch[1].Frame.PointId)
	require.Equal(t, old.Revision(), batch[1].Frame.Attributes["sim_config_revision"])
	require.Equal(t, "NEW-BPT-0002", batch[2].Frame.PointId)
	require.Equal(t, newer.Revision(), batch[2].Frame.Attributes["sim_config_revision"])
	for _, f := range batch {
		require.Equal(t, "absent", f.Frame.Attributes["sim_run_status"])
		require.NotContains(t, f.Frame.Attributes, "sim_run_id")
		require.NotContains(t, f.Frame.Attributes, "sim_step_id")
	}
	p.log = nil
	require.NoError(t, c.Sync(t.Context()))
	require.Empty(t, p.log, "304 of already applied candidate is a true no-op")
}

func TestFailuresRollbackWholeSnapshotAnd304RetriesDesired(t *testing.T) {
	for _, failure := range []string{"hold", "stop", "drain", "configure", "start", "verify", "resume", "wrong-revision"} {
		t.Run(failure, func(t *testing.T) {
			old, newer := snapshots(t)
			src := &source{s: old}
			c, p := setup(t, src)
			require.NoError(t, c.Sync(t.Context()))
			if failure == "wrong-revision" {
				p.wrongRevision = true
			} else {
				p.fail[failure] = 1
			}
			src.s = newer
			p.log = nil
			require.Error(t, c.Sync(t.Context()))
			state := c.Status()
			require.Equal(t, "retry_pending", state.Phase)
			require.Equal(t, newer.Revision(), state.DesiredRevision)
			require.Equal(t, old.Revision(), state.AppliedRevision)
			require.Equal(t, old.Revision(), state.LastGoodRevision)
			require.Equal(t, old.Native(), p.cfg)
			require.Equal(t, old.Entries(), p.resolver.Snapshot())
			require.False(t, p.held)
			require.NoError(t, c.Sync(t.Context()))
			require.Equal(t, newer.Revision(), src.known[len(src.known)-1], "HTTP conditional revision is desired, not applied")
			require.Equal(t, newer.Revision(), c.AppliedRevision())
			require.Equal(t, newer.Native(), p.cfg)
		})
	}
}

func TestRollbackFailureStaysHeldAndRetainsLastGood(t *testing.T) {
	old, newer := snapshots(t)
	src := &source{s: old}
	c, p := setup(t, src)
	require.NoError(t, c.Sync(t.Context()))
	src.s = newer
	p.fail["verify"] = 1
	p.fail["stop"] = 1 // stop error in apply followed by verify error in rollback
	require.Error(t, c.Sync(t.Context()))
	require.Equal(t, "halted", c.Status().Phase)
	require.Empty(t, c.AppliedRevision())
	require.Equal(t, old.Revision(), c.Status().LastGoodRevision)
	require.True(t, p.held)
	require.NoError(t, c.Sync(t.Context()))
	require.Equal(t, newer.Revision(), c.AppliedRevision())
}

func TestFetchErrorsAndUnknown304NeverOpenColdRuntime(t *testing.T) {
	src := &source{err: errors.New("unreachable")}
	c, p := setup(t, src)
	require.Error(t, c.Sync(t.Context()))
	require.True(t, p.held)
	require.Empty(t, c.AppliedRevision())
	require.Empty(t, c.Status().DesiredRevision)
	src.err = nil
	require.ErrorContains(t, c.Sync(t.Context()), "304 without staged")
	require.True(t, p.held)
	old, _ := snapshots(t)
	src.s = old
	p.fail["start"] = 1
	require.Error(t, c.Sync(t.Context()))
	require.Equal(t, "halted", c.Status().Phase)
	require.False(t, p.running)
	require.True(t, p.held)
	require.Empty(t, c.Status().LastGoodRevision)
	require.NoError(t, c.Sync(t.Context()))
	src.err = errors.New("403")
	p.log = nil
	require.Error(t, c.Sync(t.Context()))
	require.Equal(t, old.Revision(), c.AppliedRevision())
	require.Empty(t, p.log)
	require.False(t, p.held)
}

func TestHTTPMockFetchStageApplyAnd304(t *testing.T) {
	old, newer := snapshots(t)
	current := old
	var conditionals []string
	var lock sync.Mutex
	ca := testca.New(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		require.Equal(t, "/gateways/GW-BOPTEST-001/provisioning/v1", r.URL.Path)
		require.Empty(t, r.URL.RawQuery)
		require.Empty(t, r.Header.Get("X-Gateway-Id"))
		require.Empty(t, r.Header.Get("Authorization"))
		require.NotEmpty(t, r.TLS.VerifiedChains)
		conditionals = append(conditionals, r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", current.Revision())
		if r.Header.Get("If-None-Match") == current.Revision() {
			w.WriteHeader(304)
			return
		}
		_, _ = w.Write(current.Bytes())
	}))
	srv.TLS = ca.IssueServer(t, "localhost").ServerTLSConfig(t)
	srv.TLS.ClientCAs = ca.Pool()
	srv.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	srv.StartTLS()
	defer srv.Close()
	dir := t.TempDir()
	cert, key := ca.IssueClient(t, scope.GatewayID).WriteFiles(t, dir, "gateway")
	client, err := provisioning.NewClockHTTPClient(srv.URL, scope, provisioning.ClockHTTPOptions{TLS: provisioning.TLSOptions{CAFile: testca.WritePEM(t, dir, "ca.pem", ca.CertPEM()), CertFile: cert, KeyFile: key, ServerName: "localhost"}, Timeout: time.Second, Attempts: 1})
	require.NoError(t, err)
	c, p := setup(t, client)
	require.NoError(t, c.Sync(t.Context()))
	lock.Lock()
	current = newer
	lock.Unlock()
	p.fail["verify"] = 1
	require.Error(t, c.Sync(t.Context()))
	require.Equal(t, old.Revision(), c.AppliedRevision())
	require.NoError(t, c.Sync(t.Context()))
	require.Equal(t, newer.Revision(), c.AppliedRevision())
	lock.Lock()
	defer lock.Unlock()
	require.Equal(t, []string{"", old.Revision(), newer.Revision()}, conditionals)
}

func TestScopeMismatchRejectedBeforeLifecycleChange(t *testing.T) {
	old, _ := snapshots(t)
	src := &source{s: old}
	c, p := setup(t, src)
	require.NoError(t, c.Sync(t.Context()))
	p.log = nil
	data := strings.ReplaceAll(string(old.Bytes()), "GW-BOPTEST-001", "GW-OTHER")
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(data), &body))
	delete(body, "revision")
	delete(body, "generatedAt")
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	body["revision"] = fmt.Sprintf("\"sha256:%x\"", sha256.Sum256(raw))
	raw, err = json.Marshal(body)
	require.NoError(t, err)
	other, err := provisioning.StageClock(raw, provisioning.ClockScope{GatewayID: "GW-OTHER", ConnectorID: scope.ConnectorID, DeviceID: scope.DeviceID})
	require.NoError(t, err)
	src.s = other
	require.Error(t, c.Sync(t.Context()))
	require.Empty(t, p.log)
	require.Equal(t, old.Revision(), c.AppliedRevision())
}

type sourceFunc func(context.Context, string) (*provisioning.ClockSnapshot, error)

func (f sourceFunc) Fetch(ctx context.Context, known string) (*provisioning.ClockSnapshot, error) {
	return f(ctx, known)
}

type drainTimeout struct {
	Runtime
	once bool
}

func (r *drainTimeout) Drain(ctx context.Context) error {
	if !r.once {
		r.once = true
		<-ctx.Done()
		return ctx.Err()
	}
	return r.Runtime.Drain(ctx)
}

func TestDrainTimeoutRollsBackWithIndependentContext(t *testing.T) {
	old, newer := snapshots(t)
	src := &source{s: old}
	c, p := setup(t, src)
	require.NoError(t, c.Sync(t.Context()))
	src.s = newer
	c.timeout = 30 * time.Millisecond
	c.runtime = &drainTimeout{Runtime: c.runtime}
	require.ErrorIs(t, c.Sync(t.Context()), context.DeadlineExceeded)
	require.Equal(t, old.Revision(), c.AppliedRevision())
	require.Equal(t, old.Native(), p.cfg)
	require.False(t, p.held)
	require.NoError(t, c.Sync(t.Context()))
	require.Equal(t, newer.Revision(), c.AppliedRevision())
}

func TestCancelledApplyStopsPartialCandidateAndRestoresOld(t *testing.T) {
	old, newer := snapshots(t)
	src := &source{s: old}
	c, p := setup(t, src)
	require.NoError(t, c.Sync(t.Context()))
	src.s = newer
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p.check = func(op string) {
		if op == "start" {
			cancel()
		}
	}
	require.ErrorIs(t, c.Sync(ctx), context.Canceled)
	require.Equal(t, old.Revision(), c.AppliedRevision())
	require.Equal(t, old.Native(), p.cfg)
	require.False(t, p.held)
}

func TestConcurrentSyncRefusedAndStatusRemainsReadable(t *testing.T) {
	old, _ := snapshots(t)
	entered, release := make(chan struct{}), make(chan struct{})
	c, _ := setup(t, sourceFunc(func(ctx context.Context, known string) (*provisioning.ClockSnapshot, error) {
		close(entered)
		select {
		case <-release:
			return old, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	done := make(chan error, 1)
	go func() { done <- c.Sync(t.Context()) }()
	<-entered
	require.Empty(t, c.Status().AppliedRevision)
	require.ErrorContains(t, c.Sync(t.Context()), "already in progress")
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, old.Revision(), c.AppliedRevision())
}

func TestColdFetchTimeoutAndRunCancellation(t *testing.T) {
	c, p := setup(t, sourceFunc(func(ctx context.Context, known string) (*provisioning.ClockSnapshot, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	c.timeout = 20 * time.Millisecond
	require.ErrorIs(t, c.Sync(t.Context()), context.DeadlineExceeded)
	require.True(t, p.held)
	require.Empty(t, c.AppliedRevision())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, c.Run(ctx, time.Second), context.Canceled)
	require.Error(t, c.Run(t.Context(), 0))
}

func TestResolverRuntimeRefusesMissingBarriersAndReceipt(t *testing.T) {
	old, _ := snapshots(t)
	c, p := setup(t, &source{s: old})
	r := c.runtime.(*ResolverRuntime)
	require.Error(t, r.Configure(t.Context(), old.Native(), old.Entries()))
	require.Empty(t, p.resolver.Snapshot())
	require.NoError(t, r.Hold(t.Context()))
	require.Error(t, r.Drain(t.Context()))
	require.NoError(t, r.Stop(t.Context()))
	require.Error(t, r.Configure(t.Context(), old.Native(), old.Entries()))
	require.NoError(t, r.Drain(t.Context()))
	require.Error(t, r.Start(t.Context()))
	require.NoError(t, r.Configure(t.Context(), old.Native(), old.Entries()))
	require.NoError(t, r.Start(t.Context()))
	require.Error(t, r.Resume(t.Context()))
	require.Error(t, r.Verify(t.Context(), "wrong"))
	require.Error(t, r.Resume(t.Context()))
	require.NoError(t, r.Verify(t.Context(), old.Revision()))
	require.NoError(t, r.Resume(t.Context()))
}

func TestClockRemovalAndRollbackKeepNativeArrays(t *testing.T) {
	old, _ := snapshots(t)
	var b map[string]any
	require.NoError(t, json.Unmarshal(old.Bytes(), &b))
	b["clockSources"] = []any{}
	b["clockBindings"] = []any{}
	delete(b, "revision")
	delete(b, "generatedAt")
	data, err := json.Marshal(b)
	require.NoError(t, err)
	b["revision"] = fmt.Sprintf("\"sha256:%x\"", sha256.Sum256(data))
	data, err = json.Marshal(b)
	require.NoError(t, err)
	removed, err := provisioning.StageClock(data, scope)
	require.NoError(t, err)
	native, err := json.Marshal(removed.Native())
	require.NoError(t, err)
	require.Contains(t, string(native), `"clocks":[]`)
	src := &source{s: old}
	c, p := setup(t, src)
	require.NoError(t, c.Sync(t.Context()))
	src.s = removed
	p.fail["verify"] = 1
	require.Error(t, c.Sync(t.Context()))
	require.Equal(t, old.Revision(), c.AppliedRevision())
	require.Len(t, p.cfg.Clocks, 1)
	require.NoError(t, c.Sync(t.Context()))
	require.Equal(t, removed.Revision(), c.AppliedRevision())
	require.Empty(t, p.cfg.Clocks)
	batch, err := p.buffer.ReadBatch(0, 100)
	require.NoError(t, err)
	require.NotEmpty(t, batch)
	require.Equal(t, old.Revision(), batch[0].Frame.Attributes["sim_config_revision"])
	require.NotContains(t, batch[len(batch)-1].Frame.Attributes, "sim_time_s")
}
