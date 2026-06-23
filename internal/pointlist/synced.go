// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointlist

import (
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"unsafe"
)

// index is the immutable snapshot of the point list, swapped atomically on update.
type index struct {
	forward map[string]string // connectorID+"\x00"+localID → pointID
	reverse map[string]Entry  // pointID → Entry
	entries []Entry
}

// SyncedResolver is a thread-safe, live-updatable resolver (ADR-0003).
// It implements the Resolver interface for the Normalizer and supports reverse
// lookups (point_id → Entry) for control dispatch.
type SyncedResolver struct {
	ptr unsafe.Pointer // *index, swapped atomically
	// notify is an optional channel signaled on each Update (for tests / sync loop).
	notify chan struct{}
}

// NewSynced creates an empty SyncedResolver. notify is optional — pass nil to skip.
func NewSynced(notify chan struct{}) *SyncedResolver {
	r := &SyncedResolver{notify: notify}
	r.storeIndex(&index{
		forward: make(map[string]string),
		reverse: make(map[string]Entry),
	})
	return r
}

// Resolve implements the Resolver interface: local_id → point_id.
func (r *SyncedResolver) Resolve(connectorID, localID string) (string, bool) {
	idx := r.loadIndex()
	v, ok := idx.forward[key(connectorID, localID)]
	return v, ok
}

// ResolveReverse maps a canonical point_id back to its full Entry (for control dispatch).
func (r *SyncedResolver) ResolveReverse(pointID string) (Entry, bool) {
	idx := r.loadIndex()
	e, ok := idx.reverse[pointID]
	return e, ok
}

// Update atomically replaces the entire point list.
// Safe to call concurrently with Resolve/ResolveReverse.
func (r *SyncedResolver) Update(entries []Entry) {
	newIdx := &index{
		forward: make(map[string]string, len(entries)),
		reverse: make(map[string]Entry, len(entries)),
		entries: make([]Entry, len(entries)),
	}
	copy(newIdx.entries, entries)
	for _, e := range entries {
		newIdx.forward[key(e.ConnectorID, e.LocalID)] = e.PointID
		newIdx.reverse[e.PointID] = e
	}
	r.storeIndex(newIdx)
	if r.notify != nil {
		select {
		case r.notify <- struct{}{}:
		default:
		}
	}
}

// Snapshot returns a copy of the current entry list.
func (r *SyncedResolver) Snapshot() []Entry {
	idx := r.loadIndex()
	out := make([]Entry, len(idx.entries))
	copy(out, idx.entries)
	return out
}

// Persist writes the current snapshot to path as JSON.
func (r *SyncedResolver) Persist(path string) error {
	data, err := json.MarshalIndent(r.Snapshot(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Load reads a JSON snapshot from path and applies it.
// A missing file is silently ignored (fresh start). Any other error is returned.
func (r *SyncedResolver) Load(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	r.Update(entries)
	return nil
}

func (r *SyncedResolver) loadIndex() *index {
	return (*index)(atomic.LoadPointer(&r.ptr))
}

func (r *SyncedResolver) storeIndex(idx *index) {
	atomic.StorePointer(&r.ptr, unsafe.Pointer(idx))
}
