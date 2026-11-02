// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package clocksync implements the opt-in full-snapshot apply transaction.
// It never drives Docker or the legacy Point List sync loop implicitly.
package clocksync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
)

type Source interface {
	Fetch(context.Context, string) (*provisioning.ClockSnapshot, error)
}

// Runtime is an exclusive lifecycle/pipeline lease for the scoped connector.
// Hold closes admission of telemetry AND commands, waits for active operations,
// and stays closed on error. Drain durably processes all stopped-publisher events
// with the current mapping, including pending ACK/NAK work; queue length zero or
// a sleep is not a drain barrier. Configure runs only while held/stopped/drained.
// Start must keep new events and commands held until Verify and Resume. Verify
// checks the RUNNING connector's revision, not merely its desired env/file.
// Resume is atomic: on error admission MUST remain closed. No admin/watch path
// may bypass this lease. Implementations must honor context deadlines.
type Runtime interface {
	Hold(context.Context) error
	Stop(context.Context) error
	Drain(context.Context) error
	Configure(context.Context, provisioning.NativeClockConfig, []pointlist.Entry) error
	Start(context.Context) error
	Verify(context.Context, string) error
	Resume(context.Context) error
}

type Status struct {
	Phase            string `json:"phase"`
	DesiredRevision  string `json:"desired_revision"`
	AppliedRevision  string `json:"applied_revision"`
	LastGoodRevision string `json:"last_good_revision"`
	LastError        string `json:"last_error,omitempty"`
}

type Controller struct {
	source      Source
	runtime     Runtime
	scope       provisioning.ClockScope
	timeout     time.Duration
	serial      sync.Mutex
	mu          sync.RWMutex
	status      Status
	desired     *provisioning.ClockSnapshot
	good        *provisioning.ClockSnapshot
	initialized bool
}

func New(source Source, runtime Runtime, scope provisioning.ClockScope, timeout time.Duration) (*Controller, error) {
	if source == nil || runtime == nil || timeout <= 0 {
		return nil, fmt.Errorf("clocksync: source/runtime and positive operation timeout required")
	}
	return &Controller{source: source, runtime: runtime, scope: scope, timeout: timeout, status: Status{Phase: "uninitialized"}}, nil
}
func (c *Controller) Status() Status          { c.mu.RLock(); defer c.mu.RUnlock(); return c.status }
func (c *Controller) AppliedRevision() string { return c.Status().AppliedRevision }
func (c *Controller) update(fn func(*Status)) { c.mu.Lock(); defer c.mu.Unlock(); fn(&c.status) }
func (c *Controller) phase(p string)          { c.update(func(s *Status) { s.Phase = p }) }

// Sync serializes fetch/stage/apply/rollback. A 304 reuses the validated desired
// snapshot and retries a pending apply; it never falsely ACKs a failed revision.
func (c *Controller) Sync(ctx context.Context) error {
	if !c.serial.TryLock() {
		return fmt.Errorf("clocksync: sync already in progress")
	}
	defer c.serial.Unlock()
	if !c.initialized {
		if err := c.step(ctx, "holding", c.runtime.Hold); err != nil {
			c.update(func(s *Status) { s.Phase = "halted"; s.LastError = err.Error() })
			return err
		}
		c.phase("waiting_snapshot")
		c.initialized = true
	}
	known := ""
	if c.desired != nil {
		known = c.desired.Revision()
	}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, c.timeout)
	snapshot, err := c.source.Fetch(fetchCtx, known)
	if err == nil && fetchCtx.Err() != nil {
		err = fetchCtx.Err()
	}
	cancelFetch()
	if err != nil {
		c.update(func(s *Status) { s.LastError = err.Error() })
		return err
	}
	if snapshot != nil {
		// Validate again at this boundary, before any lifecycle side effect. This
		// also enforces scope for injected/file sources and owns all mutable data.
		snapshot, err = provisioning.StageClock(snapshot.Bytes(), c.scope)
		if err != nil {
			c.update(func(s *Status) { s.LastError = err.Error() })
			return err
		}
		c.desired = snapshot
		c.update(func(s *Status) { s.DesiredRevision = snapshot.Revision() })
	}
	if c.desired == nil {
		err := fmt.Errorf("clocksync: 304 without staged snapshot")
		c.update(func(s *Status) { s.LastError = err.Error() })
		return err
	}
	if c.AppliedRevision() == c.desired.Revision() {
		c.update(func(s *Status) { s.LastError = "" })
		return nil
	}
	c.phase("staged")
	err = c.apply(ctx, c.desired)
	if err == nil {
		c.good = c.desired
		c.update(func(s *Status) {
			s.Phase = "active"
			s.AppliedRevision = c.good.Revision()
			s.LastGoodRevision = c.good.Revision()
			s.LastError = ""
		})
		return nil
	}
	// Rollback has its own bounded context so a timed-out/cancelled apply cannot
	// abandon a partially started candidate and resume old mappings under it.
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()
	rollbackErr := c.rollback(recovery)
	err = errors.Join(err, rollbackErr)
	c.update(func(s *Status) { s.LastError = err.Error() })
	return err
}

func (c *Controller) step(parent context.Context, phase string, fn func(context.Context) error) error {
	c.phase(phase)
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clocksync %s: %w", phase, err)
	}
	if err := fn(ctx); err != nil {
		return fmt.Errorf("clocksync %s: %w", phase, err)
	}
	return nil
}

func (c *Controller) apply(ctx context.Context, s *provisioning.ClockSnapshot) error {
	// We make no runtime applied claim during a transaction, even though the
	// last confirmed coherent snapshot remains available separately.
	c.update(func(status *Status) { status.AppliedRevision = "" })
	for _, step := range []struct {
		phase string
		fn    func(context.Context) error
	}{
		{"holding", c.runtime.Hold}, {"stopping", c.runtime.Stop}, {"draining", c.runtime.Drain},
		{"configuring", func(ctx context.Context) error { return c.configure(ctx, s) }},
		{"starting", c.runtime.Start}, {"verifying", func(ctx context.Context) error { return c.runtime.Verify(ctx, s.Revision()) }},
		{"resuming", c.runtime.Resume},
	} {
		if err := c.step(ctx, step.phase, step.fn); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) rollback(ctx context.Context) error {
	c.phase("rolling_back")
	fail := func(err error) error {
		// Do not release admission after an unverified recovery. Attempt to stop
		// even a rollback instance whose startup/verification partially failed.
		_ = c.runtime.Hold(ctx)
		_ = c.runtime.Stop(ctx)
		c.update(func(s *Status) { s.Phase = "halted"; s.AppliedRevision = "" })
		return fmt.Errorf("clocksync rollback: %w", err)
	}
	// Always stop AND drain a possibly partially started candidate using its
	// still-installed mapping before restoring the old full configuration.
	for _, fn := range []func(context.Context) error{c.runtime.Hold, c.runtime.Stop, c.runtime.Drain} {
		if err := fn(ctx); err != nil {
			return fail(err)
		}
	}
	if c.good == nil {
		c.phase("halted")
		return nil
	}
	if err := c.configure(ctx, c.good); err != nil {
		return fail(err)
	}
	for _, fn := range []func(context.Context) error{c.runtime.Start, func(ctx context.Context) error { return c.runtime.Verify(ctx, c.good.Revision()) }, c.runtime.Resume} {
		if err := fn(ctx); err != nil {
			return fail(err)
		}
	}
	c.update(func(s *Status) { s.Phase = "retry_pending"; s.AppliedRevision = c.good.Revision() })
	return nil
}

func (c *Controller) configure(ctx context.Context, s *provisioning.ClockSnapshot) error {
	if runtime, ok := c.runtime.(interface {
		ConfigureSnapshot(context.Context, *provisioning.ClockSnapshot) error
	}); ok {
		return runtime.ConfigureSnapshot(ctx, s)
	}
	return c.runtime.Configure(ctx, s.Native(), s.Entries())
}

// RestoreLastGood restores only a validated rollback candidate, never a runtime
// applied claim. It is allowed only before the first Sync. The initial gate is
// still closed and RI must be fetched/verified before normal acquisition resumes.
func (c *Controller) RestoreLastGood(s *provisioning.ClockSnapshot) error {
	c.serial.Lock()
	defer c.serial.Unlock()
	if c.initialized {
		return fmt.Errorf("clocksync: recovery already started")
	}
	if s == nil {
		return nil
	}
	copy, err := provisioning.StageClock(s.Bytes(), c.scope)
	if err != nil {
		return err
	}
	c.good = copy
	c.update(func(status *Status) { status.LastGoodRevision = copy.Revision() })
	return nil
}

// WithRuntimeLease serializes explicit shutdown with a still-cleaning-up Sync.
func (c *Controller) WithRuntimeLease(ctx context.Context, fn func(context.Context) error) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if c.serial.TryLock() {
			defer c.serial.Unlock()
			return fn(ctx)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// Run performs one immediate sync and then polls. Fetch errors preserve the
// last coherent state; polling retries both fetches and failed staged applies.
func (c *Controller) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("clocksync: positive poll interval required")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = c.Sync(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
