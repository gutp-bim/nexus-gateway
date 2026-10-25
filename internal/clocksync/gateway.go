// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package clocksync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	pb "nexus-gateway/gen"
	"nexus-gateway/internal/common"
	"nexus-gateway/internal/lifecycle"
	"nexus-gateway/internal/normalizer"
	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
	"nexus-gateway/internal/storeforward"
)

// Gateway is the concrete opt-in transport admission and process adapter.
// Its worker exclusively owns the target subject; the legacy normalizer MUST
// NOT run alongside it. Archived session mappings are used even after rollback.
type Gateway struct {
	*ResolverRuntime
	nc               *nats.Conn
	consumer         jetstream.Consumer
	store            *sessions
	producer         *managedConnector
	buffer           *storeforward.Buffer
	recent           func(*pb.TelemetryFrame)
	mu               sync.Mutex
	held, draining   bool
	active, commands int
	changed          chan struct{}
	stopping         atomic.Bool
}

func NewGateway(ctx context.Context, nc *nats.Conn, js jetstream.JetStream, manager *lifecycle.Manager, spec lifecycle.ConnectorSpec, dir string, scope provisioning.ClockScope, resolver *pointlist.SyncedResolver, buffer *storeforward.Buffer, recent func(*pb.TelemetryFrame)) (*Gateway, error) {
	if nc == nil || js == nil || manager == nil || resolver == nil || buffer == nil || spec.ID != scope.ConnectorID {
		return nil, fmt.Errorf("clock gateway: invalid dependencies/scope")
	}
	store, err := openSessions(dir, scope)
	if err != nil {
		return nil, err
	}
	owner, err := manager.Claim(spec)
	if err != nil {
		return nil, err
	}
	consumer, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{Durable: "clock-" + scope.ConnectorID, FilterSubject: "evt.bacnet." + scope.ConnectorID, AckPolicy: jetstream.AckExplicitPolicy, AckWait: 2 * time.Minute, MaxDeliver: -1})
	if err != nil {
		return nil, err
	}
	g := &Gateway{nc: nc, consumer: consumer, store: store, producer: &managedConnector{nc: nc, owner: owner, base: spec, store: store}, buffer: buffer, recent: recent, held: true, changed: make(chan struct{})}
	g.ResolverRuntime, err = NewResolverRuntime(g, g.producer, resolver)
	if err != nil {
		return nil, err
	}
	g.producer.stopping = &g.stopping
	return g, nil
}

func (g *Gateway) notify() { close(g.changed); g.changed = make(chan struct{}) }

// Enter protects native command resolution AND the complete write/reply against
// mapping replacement. Refused commands are not queued for later execution.
func (g *Gateway) Enter(ctx context.Context) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held || ctx.Err() != nil {
		return nil, false
	}
	g.commands++
	return func() { g.mu.Lock(); g.commands--; g.notify(); g.mu.Unlock() }, true
}

func (g *Gateway) Hold(ctx context.Context) error {
	g.mu.Lock()
	g.held = true
	g.notify()
	for g.active > 0 || g.commands > 0 {
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		g.mu.Lock()
	}
	g.mu.Unlock()
	if !g.producer.discovered {
		if err := g.producer.Stop(ctx); err != nil {
			return err
		}
	}
	if g.producer.started {
		r, err := g.producer.request(ctx, "quiesce")
		if err != nil {
			_ = g.producer.Stop(ctx)
			return err
		}
		if !*r.Paused {
			return fmt.Errorf("producer quiesce unconfirmed")
		}
	}
	return nil
}

func (g *Gateway) Drain(ctx context.Context) error {
	g.mu.Lock()
	g.draining = true
	g.notify()
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.draining = false; g.notify(); g.mu.Unlock() }()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		info, err := g.consumer.Info(ctx)
		if err != nil {
			return err
		}
		g.mu.Lock()
		idle := g.active == 0
		g.mu.Unlock()
		if info.NumPending == 0 && info.NumAckPending == 0 && idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (g *Gateway) ConfigureSnapshot(ctx context.Context, snapshot *provisioning.ClockSnapshot) error {
	r, err := g.store.create(snapshot)
	if err != nil {
		return err
	}
	g.producer.record = r
	return g.ResolverRuntime.Configure(ctx, snapshot.Native(), snapshot.Entries())
}

func (g *Gateway) Resume(ctx context.Context) error {
	if g.stopping.Load() {
		return fmt.Errorf("gateway shutdown requested")
	}
	// Called by ResolverRuntime only after its observed runtime receipt succeeds.
	r, err := g.producer.request(ctx, "resume")
	if err != nil {
		return err
	}
	if *r.Paused {
		return fmt.Errorf("producer resume unconfirmed")
	}
	if g.stopping.Load() {
		return fmt.Errorf("gateway shutdown requested")
	}
	if err = g.store.commit(g.producer.record); err != nil {
		return err
	}
	g.mu.Lock()
	if g.stopping.Load() {
		g.mu.Unlock()
		return fmt.Errorf("gateway shutdown requested")
	}
	g.held = false
	g.notify()
	g.mu.Unlock()
	return nil
}

// Runtime avoids embedding dispatch ambiguity: the controller must use the
// barrier-checking ResolverRuntime, while its Snapshot configure hook stays here.
type gatewayRuntime struct {
	*ResolverRuntime
	gateway *Gateway
}

func (r gatewayRuntime) ConfigureSnapshot(ctx context.Context, s *provisioning.ClockSnapshot) error {
	return r.gateway.ConfigureSnapshot(ctx, s)
}
func (g *Gateway) Runtime() Runtime {
	return gatewayRuntime{ResolverRuntime: g.ResolverRuntime, gateway: g}
}
func (g *Gateway) LastGood() (*provisioning.ClockSnapshot, error) { return g.store.lastGood() }

func (g *Gateway) Run(ctx context.Context) error {
	for {
		g.mu.Lock()
		allowed := !g.held || g.draining
		changed := g.changed
		g.mu.Unlock()
		if !allowed {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
				continue
			}
		}
		batch, err := g.consumer.Fetch(1, jetstream.FetchMaxWait(100*time.Millisecond))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		for msg := range batch.Messages() {
			g.mu.Lock()
			allowed = !g.held || g.draining
			if allowed {
				g.active++
			}
			g.mu.Unlock()
			if !allowed {
				_ = msg.NakWithDelay(100 * time.Millisecond)
				continue
			}
			err = g.process(ctx, msg)
			g.mu.Lock()
			g.active--
			g.notify()
			g.mu.Unlock()
			if err != nil {
				slog.Warn("clock worker: durable processing failed", "err", err)
				_ = msg.NakWithDelay(time.Second)
			}
		}
		if err := batch.Error(); err != nil && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, jetstream.ErrNoMessages) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (g *Gateway) process(ctx context.Context, msg jetstream.Msg) error {
	md, err := msg.Metadata()
	if err != nil {
		return err
	}
	r, ok := g.store.get(msg.Headers().Get("Nexus-Session"))
	reason := "unknown or unverified session"
	var evt common.Event
	if ok && r.verified && msg.Headers().Get("Nexus-Config-Revision") == r.snapshot.Revision() && json.Unmarshal(msg.Data(), &evt) == nil {
		if frame, valid := g.boundFrame(evt, r); valid {
			if err = g.buffer.Write(frame); err != nil {
				return err
			}
			if g.recent != nil {
				g.recent(frame)
			}
			return msg.DoubleAck(ctx)
		}
		reason = "event does not match its archived session"
	}
	// Persist raw bytes (base64 in JSON) before terminating poison/unverified
	// delivery. EVENTS retention remains intact; no raw message is deleted.
	data, err := json.Marshal(struct {
		Reason  string
		Subject string
		Headers nats.Header
		Data    []byte
	}{reason, msg.Subject(), msg.Headers(), msg.Data()})
	if err != nil {
		return err
	}
	if err = atomicFile(filepath.Join(g.store.dir, "quarantine", fmt.Sprintf("%d.json", md.Sequence.Stream)), data); err != nil {
		return err
	}
	slog.Warn("clock event quarantined", "sequence", md.Sequence.Stream, "reason", reason)
	return msg.Term()
}

func (g *Gateway) boundFrame(evt common.Event, r *sessionRecord) (*pb.TelemetryFrame, bool) {
	if evt.Protocol != "bacnet" || evt.ConnectorID != g.store.scope.ConnectorID || evt.Timestamp == "" {
		return nil, false
	}
	if _, err := time.Parse(time.RFC3339Nano, evt.Timestamp); err != nil {
		return nil, false
	}
	for _, key := range []string{"sim_run_id", "sim_step_id", "sim_context_generation"} {
		if _, ok := evt.Attributes[key]; ok {
			return nil, false
		}
	}
	if status := evt.Attributes["sim_run_status"]; status != "" && status != "absent" {
		return nil, false
	}
	var point *pointlist.Entry
	entries := r.snapshot.Entries()
	for i := range entries {
		if entries[i].LocalID == evt.LocalID {
			point = &entries[i]
			break
		}
	}
	if point == nil || point.DeviceRef != evt.DeviceRef {
		return nil, false
	}
	clocked := false
	for _, clock := range r.snapshot.Native().Clocks {
		for _, id := range clock.PointLocalIDs {
			if id == evt.LocalID {
				clocked = true
				if evt.Attributes["sim_clock_id"] != clock.ClockID || evt.Attributes["sim_config_revision"] != r.snapshot.Revision() || evt.Attributes["sim_run_status"] != "absent" {
					return nil, false
				}
			}
		}
	}
	if !clocked {
		for key := range evt.Attributes {
			if len(key) >= 4 && key[:4] == "sim_" {
				return nil, false
			}
		}
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return nil, false
	}
	frame, out := normalizer.Normalize(data, pointlist.NewFixture(entries), g.store.scope.GatewayID)
	return frame, out == normalizer.OutcomeOK
}

func (g *Gateway) Shutdown(ctx context.Context) error {
	g.RequestStop()
	// Always attempt termination even if quiesce times out, keeping admission held.
	holdErr := g.Hold(ctx)
	stopErr := g.producer.Stop(ctx)
	if stopErr != nil {
		return stopErr
	}
	if holdErr != nil && ctx.Err() != nil {
		return holdErr
	}
	return g.Drain(ctx)
}

func (g *Gateway) RequestStop() {
	g.mu.Lock()
	g.stopping.Store(true)
	g.held = true
	g.notify()
	g.mu.Unlock()
}
