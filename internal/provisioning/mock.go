// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"nexus-gateway/internal/pointlist"
)

// Mock is an in-memory provisioning.Client for tests and development.
// Call SetEntries to update the snapshot and bump the ETag atomically.
type Mock struct {
	mu         sync.Mutex
	entries    []pointlist.Entry
	etag       string
	fetchCalls atomic.Int64
}

// NewMock creates a Mock with an initial snapshot.
func NewMock(entries []pointlist.Entry) *Mock {
	m := &Mock{}
	m.setEntriesLocked(entries)
	return m
}

// SetEntries replaces the snapshot and updates the ETag.
func (m *Mock) SetEntries(entries []pointlist.Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setEntriesLocked(entries)
}

// SetSnapshot is an alias for SetEntries for backward compatibility.
func (m *Mock) SetSnapshot(entries []pointlist.Entry) { m.SetEntries(entries) }

func (m *Mock) setEntriesLocked(entries []pointlist.Entry) {
	m.entries = make([]pointlist.Entry, len(entries))
	copy(m.entries, entries)
	data, _ := json.Marshal(entries)
	m.etag = fmt.Sprintf("%x", sha256.Sum256(data))[:16]
}

// Fetch implements Client. Returns nil when knownETag matches (304).
// Always returns a full result (no delta support for Mock).
func (m *Mock) Fetch(_ context.Context, knownETag string) (*FetchResult, error) {
	m.fetchCalls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	if knownETag == m.etag {
		return nil, nil // 304 — unchanged
	}
	out := make([]pointlist.Entry, len(m.entries))
	copy(out, m.entries)
	return &FetchResult{ETag: m.etag, Full: true, Entries: out}, nil
}

// FetchCalls returns the number of Fetch calls made so far.
func (m *Mock) FetchCalls() int { return int(m.fetchCalls.Load()) }

// CurrentETag returns the current ETag of the mock's snapshot.
func (m *Mock) CurrentETag() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.etag
}

// VersionCalls is kept for tests that haven't migrated yet (always returns FetchCalls).
func (m *Mock) VersionCalls() int { return m.FetchCalls() }

// SnapshotCalls returns 0; the new Fetch-based interface merges version + snapshot into one call.
// Tests should migrate to FetchCalls.
func (m *Mock) SnapshotCalls() int { return 0 }
