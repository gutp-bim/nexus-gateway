// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package clocksync

import (
	"context"
	"fmt"

	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
)

// Admission owns a target connector's telemetry/control gate and durable drain
// barrier. Other connectors may keep running. Hold/Resume must be atomic and
// exclusive with administration/watch restart and command dispatch.
// Drain must verify the event revision/session binding before normalization;
// mismatched/unverified generation output stays quarantined, never remapped.
type Admission interface {
	Hold(context.Context) error
	Drain(context.Context) error
	Resume(context.Context) error
}

// BACnetLifecycle configures an already-installed, exclusively leased connector;
// it must not pull images or treat a desired file as an observed runtime receipt.
// Start retains output behind Admission until Resume. Stop confirms termination
// of every previous instance. Revision reports the loaded RUNNING revision.
type BACnetLifecycle interface {
	Stop(context.Context) error
	Configure(context.Context, provisioning.NativeClockConfig) error
	Start(context.Context) error
	Revision(context.Context) (string, error)
}

// ResolverRuntime performs real mapping replacement while the admission gate
// is closed, publishers stopped and old durable work drained. Low-level process
// management and the transport-specific ACK barrier are explicit adapters.
// The legacy lifecycle.Manager alone does NOT satisfy these stronger contracts.
type ResolverRuntime struct {
	gate                   Admission
	connector              BACnetLifecycle
	resolver               *pointlist.SyncedResolver
	held, stopped, drained bool
	configuredRevision     string
	verified               bool
}

func NewResolverRuntime(gate Admission, connector BACnetLifecycle, resolver *pointlist.SyncedResolver) (*ResolverRuntime, error) {
	if gate == nil || connector == nil || resolver == nil {
		return nil, fmt.Errorf("clocksync: admission, lifecycle and resolver required")
	}
	return &ResolverRuntime{gate: gate, connector: connector, resolver: resolver}, nil
}
func (r *ResolverRuntime) Hold(ctx context.Context) error {
	if err := r.gate.Hold(ctx); err != nil {
		return err
	}
	r.held = true
	return nil
}
func (r *ResolverRuntime) Stop(ctx context.Context) error {
	if !r.held {
		return fmt.Errorf("clocksync: stop requires held admission")
	}
	if err := r.connector.Stop(ctx); err != nil {
		return err
	}
	r.stopped = true
	r.drained = false
	r.verified = false
	return nil
}
func (r *ResolverRuntime) Drain(ctx context.Context) error {
	if !r.held || !r.stopped {
		return fmt.Errorf("clocksync: drain requires stopped publishers")
	}
	if err := r.gate.Drain(ctx); err != nil {
		return err
	}
	r.drained = true
	return nil
}
func (r *ResolverRuntime) Configure(ctx context.Context, native provisioning.NativeClockConfig, entries []pointlist.Entry) error {
	if !r.held || !r.stopped || !r.drained {
		return fmt.Errorf("clocksync: configure requires stop/drain barrier")
	}
	if err := r.connector.Configure(ctx, native); err != nil {
		return err
	}
	r.resolver.Update(entries)
	r.configuredRevision = native.Revision
	r.verified = false
	return nil
}
func (r *ResolverRuntime) Start(ctx context.Context) error {
	if !r.held || !r.stopped || !r.drained || r.configuredRevision == "" {
		return fmt.Errorf("clocksync: start requires held prepared configuration")
	}
	// Even an unsuccessful Start may have spawned a partial instance.
	r.stopped = false
	r.drained = false
	return r.connector.Start(ctx)
}
func (r *ResolverRuntime) Verify(ctx context.Context, revision string) error {
	r.verified = false
	if !r.held || r.stopped {
		return fmt.Errorf("clocksync: verify requires held running connector")
	}
	got, err := r.connector.Revision(ctx)
	if err != nil {
		return err
	}
	if got != revision || revision != r.configuredRevision {
		return fmt.Errorf("clocksync: running connector revision mismatch")
	}
	r.verified = true
	return nil
}
func (r *ResolverRuntime) Resume(ctx context.Context) error {
	if !r.held || r.stopped || !r.verified {
		return fmt.Errorf("clocksync: resume requires running connector")
	}
	if err := r.gate.Resume(ctx); err != nil {
		return err
	}
	r.held = false
	return nil
}
