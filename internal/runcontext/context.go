// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package runcontext verifies a signed run lease against a durable receiver
// ledger. It does not fetch leases, infer simulator state, or attach telemetry.
package runcontext

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const schema = "clock-run-context/2-draft"

type Scope struct {
	GatewayID string `json:"gatewayId"`
	ClockID   string `json:"clockId"`
}
type Context struct {
	SchemaVersion        string    `json:"schemaVersion"`
	GatewayID            string    `json:"gatewayId"`
	ClockID              string    `json:"clockId"`
	ProvisioningRevision string    `json:"provisioningRevision"`
	RunID                string    `json:"runId"`
	Generation           uint64    `json:"generation"`
	Status               string    `json:"status"`
	IssuedAt             time.Time `json:"issuedAt"`
	ValidFrom            time.Time `json:"validFrom"`
	ExpiresAt            time.Time `json:"expiresAt"`
}

// Payload is base64-encoded by JSON so its signed bytes survive persistence.
type Envelope struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}
type Result struct {
	Status, Reason, RunID string
	Generation            uint64
}
type state struct {
	InvalidRuns map[string]bool `json:"invalidRuns"`
	Version     int             `json:"version"`
	Scope       Scope           `json:"scope"`
	PublicKey   []byte          `json:"publicKey"`
	Envelope    *Envelope       `json:"envelope"`
	CommittedAt time.Time       `json:"committedAt"`
	AppliedAt   *time.Time      `json:"appliedAt"`
}
type Ledger struct {
	failed  bool
	mu      sync.Mutex
	path    string
	data    state
	context *Context
}

// Initialize is an explicit new authority-domain operation, never a recovery
// fallback. The caller must exclusively own the ledger for its whole lifetime.
func Initialize(path string, scope Scope, key ed25519.PublicKey) (*Ledger, error) {
	if scope.GatewayID == "" || scope.ClockID == "" || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("invalid scope/key")
	}
	data := state{Version: 1, InvalidRuns: map[string]bool{}, Scope: scope, PublicKey: append([]byte(nil), key...)}
	raw, _ := json.Marshal(data)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncDirectory(path)
	}
	if err != nil {
		return nil, err
	}
	return &Ledger{path: path, data: data}, nil
}

func Open(path string, scope Scope, key ed25519.PublicKey) (*Ledger, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data state
	if err = decode(raw, &data); err != nil {
		return nil, err
	}
	if data.InvalidRuns == nil || data.Version != 1 || data.Scope != scope || len(key) != ed25519.PublicKeySize || !bytes.Equal(data.PublicKey, key) {
		return nil, errors.New("ledger scope/key mismatch")
	}
	ledger := &Ledger{path: path, data: data}
	if data.Envelope != nil {
		context, err := ledger.verify(*data.Envelope)
		if err != nil {
			return nil, err
		}
		if data.CommittedAt.Before(context.IssuedAt) || !data.CommittedAt.Before(context.ExpiresAt) {
			return nil, errors.New("invalid persisted commit")
		}
		if data.AppliedAt != nil && (data.AppliedAt.Before(data.CommittedAt) || !data.AppliedAt.Before(context.ExpiresAt)) {
			return nil, errors.New("invalid persisted apply")
		}
		ledger.context = &context
	} else if !data.CommittedAt.IsZero() || data.AppliedAt != nil {
		return nil, errors.New("orphan ledger receipts")
	}
	return ledger, nil
}

func decode(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func (l *Ledger) verify(envelope Envelope) (Context, error) {
	var context Context
	if len(envelope.Payload) > 65536 || !ed25519.Verify(l.data.PublicKey, envelope.Payload, envelope.Signature) {
		return context, errors.New("invalid run context signature")
	}
	if err := decode(envelope.Payload, &context); err != nil {
		return context, err
	}
	if context.SchemaVersion != schema || context.GatewayID != l.data.Scope.GatewayID || context.ClockID != l.data.Scope.ClockID || context.RunID == "" || context.ProvisioningRevision == "" || context.Generation < 1 || context.Generation > 9007199254740991 || (context.Status != "active" && context.Status != "revoked") || context.IssuedAt.IsZero() || context.IssuedAt.After(context.ValidFrom) || !context.ValidFrom.Before(context.ExpiresAt) {
		return context, errors.New("invalid run context contract")
	}
	for _, at := range []time.Time{context.IssuedAt, context.ValidFrom, context.ExpiresAt} {
		_, offset := at.Zone()
		if offset != 0 {
			return context, errors.New("context times must be UTC")
		}
	}
	return context, nil
}

// Commit persists authentication and generation before any apply receipt.
func (l *Ledger) Commit(envelope Envelope, at time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed {
		return errors.New("run ledger requires recovery")
	}
	context, err := l.verify(envelope)
	if err != nil {
		return err
	}
	if at.Before(context.IssuedAt) || !at.Before(context.ExpiresAt) {
		return errors.New("context outside commit lifetime")
	}
	if l.context == nil && context.Generation != 1 {
		return errors.New("new domain requires generation one")
	}
	if l.context != nil {
		if context.Generation < l.context.Generation {
			return errors.New("generation rollback")
		}
		if context.Generation == l.context.Generation {
			old, _ := json.Marshal(l.context)
			next, _ := json.Marshal(context)
			if !bytes.Equal(old, next) {
				return errors.New("generation conflict")
			}
			return nil
		}
	}
	next := l.data
	next.Envelope = &Envelope{Payload: append([]byte(nil), envelope.Payload...), Signature: append([]byte(nil), envelope.Signature...)}
	next.CommittedAt = at
	next.AppliedAt = nil
	if context.Status == "revoked" {
		next.InvalidRuns = copyInvalidRuns(l.data.InvalidRuns)
		next.InvalidRuns[context.RunID] = true
	}
	if err = l.persist(next); err != nil {
		return err
	}
	l.context = &context
	return nil
}

func (l *Ledger) Apply(at time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed {
		return errors.New("run ledger requires recovery")
	}
	if l.context == nil || at.Before(l.data.CommittedAt) || !at.Before(l.context.ExpiresAt) {
		return errors.New("invalid apply boundary")
	}
	if l.data.AppliedAt != nil {
		return nil
	}
	next := l.data
	next.AppliedAt = &at
	return l.persist(next)
}

// Evaluate uses the complete acquisition interval, not delivery time. Callers
// must archive the result at acquisition; do not reevaluate replay with today’s lease.
func (l *Ledger) Evaluate(revision string, start, end time.Time) Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed {
		return Result{Status: "unverified", Reason: "generation_history_unavailable"}
	}
	if l.context == nil {
		return Result{Status: "absent"}
	}
	c := l.context
	invalid := func(reason string) Result { return Result{Status: "invalid", Reason: reason} }
	if end.Before(start) {
		return invalid("invalid_acquisition_interval")
	}
	if c.ProvisioningRevision != revision {
		return invalid("revision_mismatch")
	}
	if c.Status == "revoked" {
		return invalid("revoked")
	}
	if start.Before(c.ValidFrom) {
		return invalid("not_yet_valid")
	}
	if !end.Before(c.ExpiresAt) {
		return invalid("expired")
	}
	if l.data.AppliedAt == nil {
		return Result{Status: "unverified", Reason: "context_not_applied"}
	}
	if start.Before(*l.data.AppliedAt) {
		return invalid("context_not_active_at_start")
	}
	if l.data.InvalidRuns[c.RunID] {
		return invalid("reset_detected")
	}

	return Result{Status: "valid", RunID: c.RunID, Generation: c.Generation}
}

// InvalidateRun records reset/revocation permanently before admitting more data.
func (l *Ledger) InvalidateRun(runID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed {
		return errors.New("run ledger requires recovery")
	}
	if l.context == nil || runID != l.context.RunID {
		return errors.New("unknown current run")
	}
	next := l.data
	next.InvalidRuns = copyInvalidRuns(l.data.InvalidRuns)
	next.InvalidRuns[runID] = true
	return l.persist(next)
}
func copyInvalidRuns(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (l *Ledger) persist(next state) (result error) {
	defer func() {
		if result != nil {
			l.failed = true
		}
	}()
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(l.path), ".run-stage-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), l.path)
	}
	if err == nil {
		err = syncDirectory(l.path)
	}
	if err != nil {
		return fmt.Errorf("run ledger persistence: %w", err)
	}
	l.data = next
	return nil
}
func syncDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Archive records a new acquisition activation boundary in an immutable,
// receiver-owned session journal. The caller must still hold producer admission.
func (l *Ledger) Archive(path string, appliedAt time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed || l.context == nil || l.data.AppliedAt == nil || appliedAt.Before(l.data.CommittedAt) || !appliedAt.Before(l.context.ExpiresAt) {
		return errors.New("run context not ready for session archive")
	}
	next := l.data
	next.AppliedAt = &appliedAt
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncDirectory(path)
	}
	return err
}

// Current exposes an authenticated copy, never a pointer to live state.
func (l *Ledger) Current() (Context, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed || l.context == nil {
		return Context{}, errors.New("run history unavailable")
	}
	return *l.context, nil
}

func (l *Ledger) Invalidated(runID string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed {
		return false, errors.New("run ledger requires recovery")
	}
	return l.data.InvalidRuns[runID], nil
}

// SignedEnvelope returns an authenticated copy for a receiver-owned archive.
func (l *Ledger) SignedEnvelope() (Envelope, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed || l.data.Envelope == nil {
		return Envelope{}, errors.New("run history unavailable")
	}
	return Envelope{Payload: append([]byte(nil), l.data.Envelope.Payload...), Signature: append([]byte(nil), l.data.Envelope.Signature...)}, nil
}

// InvalidateContext also accepts an authenticated historical run in this same
// authority domain, so delayed reset evidence cannot be cleared by a newer run.
func (l *Ledger) InvalidateContext(envelope Envelope) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed || l.context == nil {
		return errors.New("run history unavailable")
	}
	context, err := l.verify(envelope)
	if err != nil {
		return err
	}
	if context.Generation > l.context.Generation {
		return errors.New("uncommitted reset context")
	}
	next := l.data
	next.InvalidRuns = copyInvalidRuns(l.data.InvalidRuns)
	next.InvalidRuns[context.RunID] = true
	return l.persist(next)
}

// ReceiverTimes exposes only acquisition activation evidence, not the signed
// context payload or signature. Signed files stay in the pre-distributed path.
func (l *Ledger) ReceiverTimes() (time.Time, *time.Time, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed || l.context == nil {
		return time.Time{}, nil, errors.New("run history unavailable")
	}
	if l.data.AppliedAt == nil {
		return l.data.CommittedAt, nil, nil
	}
	applied := *l.data.AppliedAt
	return l.data.CommittedAt, &applied, nil
}
