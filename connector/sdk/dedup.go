// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package sdk

import "sync"

// CommandDedup handles per-command idempotency for connector write handlers (ADR-0005).
//
// It uses a nil-sentinel pattern: a nil entry means "in-flight" (first goroutine is
// writing); a non-nil entry holds the cached result. The map is bounded: when cap is
// reached the oldest entry is evicted so memory usage stays O(cap).
type CommandDedup struct {
	mu      sync.Mutex
	entries map[string]*WriteReply
	keys    []string // insertion order for bounded eviction
	cap     int
}

// NewCommandDedup creates a CommandDedup with the given capacity.
// When the map reaches cap entries, the oldest is evicted on the next Reserve.
func NewCommandDedup(cap int) *CommandDedup {
	return &CommandDedup{
		entries: make(map[string]*WriteReply, cap),
		cap:     cap,
	}
}

// TryReserve claims a slot for controlID. Callers must inspect the return values:
//   - (true, nil)    → proceed with the write; call Complete when done.
//   - (false, nil)   → another goroutine is writing (in-flight); caller should return "in_flight".
//   - (false, reply) → write already completed; return the cached reply.
func (d *CommandDedup) TryReserve(controlID string) (proceed bool, cached *WriteReply) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if entry, ok := d.entries[controlID]; ok {
		if entry == nil {
			return false, nil // in-flight
		}
		return false, entry // cached
	}

	// Evict oldest if at capacity.
	if len(d.keys) >= d.cap {
		oldest := d.keys[0]
		d.keys = d.keys[1:]
		delete(d.entries, oldest)
	}

	d.entries[controlID] = nil // reserve slot (in-flight sentinel)
	d.keys = append(d.keys, controlID)
	return true, nil
}

// Complete stores the result for controlID and lifts the in-flight sentinel.
// Safe to call from any goroutine after TryReserve returned proceed=true.
func (d *CommandDedup) Complete(controlID string, reply WriteReply) {
	d.mu.Lock()
	d.entries[controlID] = &reply
	d.mu.Unlock()
}
