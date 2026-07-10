// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package adminapi

import (
	"sync"
	"time"

	pb "nexus-gateway/gen"
)

// RecentEntry holds the latest value observed for a single point.
type RecentEntry struct {
	PointID    string  `json:"point_id"`
	Value      float64 `json:"value"`
	Timestamp  string  `json:"timestamp"`
	ReceivedAt string  `json:"received_at"`
}

// RecentStore tracks the most recent telemetry value per point_id in memory.
// It is intentionally ephemeral: values are lost on gateway restart.
// Thread-safe for concurrent Record / Snapshot access.
type RecentStore struct {
	mu      sync.RWMutex
	entries map[string]RecentEntry
}

// NewRecentStore creates an empty RecentStore.
func NewRecentStore() *RecentStore {
	return &RecentStore{entries: make(map[string]RecentEntry)}
}

// Record updates the stored value for the frame's point_id.
func (s *RecentStore) Record(f *pb.TelemetryFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[f.PointId] = RecentEntry{
		PointID:    f.PointId,
		Value:      f.Value,
		Timestamp:  f.Timestamp,
		ReceivedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// Snapshot returns all currently tracked entries, sorted by point_id.
func (s *RecentStore) Snapshot() []RecentEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RecentEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	return out
}
