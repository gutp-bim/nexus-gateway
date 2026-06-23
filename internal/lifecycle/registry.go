// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"
	"sync"
)

// ErrConnectorNotFound is returned when an operation targets an ID that is not in the registry.
var ErrConnectorNotFound = errors.New("lifecycle: connector not found")

// ConnectorSpec describes a connector installation.
type ConnectorSpec struct {
	ID          string   // unique connector ID, e.g. "mqtt-01"
	Image       string   // current digest-pinned image reference (e.g. "reg/img@sha256:…")
	PrevImage   string   // previous digest-pinned image, retained for rollback (ADR-0006); "" if none
	Env         []string // environment variables to inject into the container
	Permissions ConnectorPermissions
}

// ConnectorPermissions mirrors catalog.Permissions and declares the container capabilities.
type ConnectorPermissions struct {
	Network []string // allowed network segments, for documentation/audit
	Mounts  []string // host paths to bind-mount into the container (read-only)
}

// ConnectorStatus is the runtime state of a connector in the registry.
type ConnectorStatus struct {
	Spec        ConnectorSpec
	ContainerID string // Docker container ID, or "" if not running
	Running     bool
}

// Registry holds the set of installed connectors and their runtime status.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*ConnectorStatus
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]*ConnectorStatus)}
}

// Register adds or replaces a connector specification. The connector is initially not running.
func (r *Registry) Register(spec ConnectorSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.entries[spec.ID]; ok {
		existing.Spec = spec
	} else {
		r.entries[spec.ID] = &ConnectorStatus{Spec: spec}
	}
}

// SetRunning updates the runtime state of a connector.
func (r *Registry) SetRunning(id, containerID string, running bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[id]; ok {
		e.ContainerID = containerID
		e.Running = running
	}
}

// Remove deletes a connector from the registry.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, id)
}

// List returns a snapshot of all connector statuses.
func (r *Registry) List() []ConnectorStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ConnectorStatus, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, *e)
	}
	return out
}

// Get returns the status for a specific connector.
func (r *Registry) Get(id string) (ConnectorStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return ConnectorStatus{}, false
	}
	return *e, true
}
