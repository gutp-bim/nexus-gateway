// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointsync

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
)

// Config holds the sync loop parameters.
type Config struct {
	Interval    time.Duration
	PersistPath string // path to persist the snapshot; empty = no persistence
}

// Loop polls the provisioning API and keeps a SyncedResolver up to date (ADR-0003).
// It uses the ETag-based Fetch interface (#224): 304 means no-op, a diff result is
// applied incrementally, a full result replaces the resolver entirely.
type Loop struct {
	client     provisioning.Client
	resolver   *pointlist.SyncedResolver
	cfg        Config
	revalidate <-chan struct{} // optional push signals (EgressDown.point_list_update)
	ready      chan struct{}   // closed when first sync attempt completes

	mu          sync.RWMutex // guards appliedETag
	appliedETag string       // ETag currently applied to the resolver ("" until the first successful sync)
}

// New creates a Loop.
func New(client provisioning.Client, resolver *pointlist.SyncedResolver, cfg Config) *Loop {
	return &Loop{client: client, resolver: resolver, cfg: cfg, ready: make(chan struct{})}
}

// Ready returns a channel that is closed after the first sync attempt completes,
// whether it succeeded or not. Callers can select on this instead of polling
// resolver.Snapshot(), and combine with time.After for a startup timeout.
func (l *Loop) Ready() <-chan struct{} {
	return l.ready
}

// WithRevalidate attaches a channel whose sends trigger an immediate re-sync
// (used by the egress agent on EgressDown.point_list_update). Returns l for chaining.
func (l *Loop) WithRevalidate(ch <-chan struct{}) *Loop {
	l.revalidate = ch
	return l
}

// AppliedRevision returns the point-list ETag currently applied to the resolver, or "" if no sync
// has succeeded yet. Thread-safe: the egress agent reads it to report sync state up the stream
// (#230 Phase 2b, ADR-0004 option A), so it satisfies egress.RevisionProvider.
func (l *Loop) AppliedRevision() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.appliedETag
}

// Run polls the provisioning API until ctx is cancelled.
// It syncs once on startup (blocking), then re-syncs on the ticker or revalidate signal.
func (l *Loop) Run(ctx context.Context) {
	if l.cfg.PersistPath != "" {
		if err := l.resolver.Load(l.cfg.PersistPath); err != nil {
			slog.Warn("pointsync: load persisted snapshot failed", "err", err)
		}
	}

	state := &syncState{}

	// Force immediate first sync; signal Ready() after it completes (success or error).
	l.sync(ctx, state)
	close(l.ready)

	tick := time.NewTicker(l.cfg.Interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			l.sync(ctx, state)
		case _, ok := <-l.revalidate:
			if !ok {
				return
			}
			l.sync(ctx, state)
		}
	}
}

// syncState carries the mutable state threaded through sync calls.
type syncState struct {
	etag    string
	entries []pointlist.Entry // cached full list for diff application
}

func (l *Loop) sync(ctx context.Context, s *syncState) {
	result, err := l.client.Fetch(ctx, s.etag)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("pointsync: fetch error", "err", err)
		}
		return
	}
	if result == nil {
		return // 304 — unchanged
	}

	if result.Full {
		s.entries = result.Entries
	} else {
		s.entries = applyDiff(s.entries, result)
	}

	l.resolver.Update(s.entries)
	s.etag = result.ETag
	l.mu.Lock()
	l.appliedETag = result.ETag
	l.mu.Unlock()
	slog.Info("pointsync: point list updated", "etag", result.ETag, "count", len(s.entries))

	if l.cfg.PersistPath != "" {
		if err := l.resolver.Persist(l.cfg.PersistPath); err != nil {
			slog.Warn("pointsync: persist failed", "err", err)
		}
	}
}

// applyDiff merges added/removed/changed into the current entry slice.
func applyDiff(current []pointlist.Entry, r *provisioning.FetchResult) []pointlist.Entry {
	// Index by pointID for O(1) lookup. Also build a secondary index by
	// (connectorID, localID) so we can remove stale entries when a PointID is renamed.
	byID := make(map[string]pointlist.Entry, len(current))
	byLocal := make(map[string]string, len(current)) // connectorID+"\x00"+localID → pointID
	for _, e := range current {
		byID[e.PointID] = e
		byLocal[e.ConnectorID+"\x00"+e.LocalID] = e.PointID
	}
	for _, pid := range r.Removed {
		delete(byID, pid)
	}
	for _, e := range r.Added {
		byID[e.PointID] = e
	}
	for _, e := range r.Changed {
		// If the PointID was renamed, delete the old entry so the resolver does not
		// see two entries for the same (connectorID, localID) with different pointIDs
		// (map iteration order is non-deterministic → flaky Resolve results).
		if oldPID := byLocal[e.ConnectorID+"\x00"+e.LocalID]; oldPID != "" && oldPID != e.PointID {
			delete(byID, oldPID)
		}
		byID[e.PointID] = e
	}
	out := make([]pointlist.Entry, 0, len(byID))
	for _, e := range byID {
		out = append(out, e)
	}
	return out
}
