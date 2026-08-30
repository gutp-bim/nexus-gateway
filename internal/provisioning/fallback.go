// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"sync"
)

// FallbackClient composes a primary provisioning.Client (Building OS) and a
// secondary one (typically a local file/CSV bootstrap) into a single Client,
// implementing the dynamic source precedence described in ADR-0003 and scoped
// in docs/backlog/epic/EP-013-provisioning-dynamic-fallback.md: "the gateway
// may bootstrap its Point List from a local file when the provisioning API is
// unreachable, but the authoritative provisioning snapshot always overrides
// the file once synced."
//
// Before the primary has ever answered successfully, Fetch tries it first
// (always with an empty knownETag — the caller's knownETag belongs to the
// secondary's ETag namespace and would be meaningless to the primary) and
// falls back to the secondary on any primary error. The first successful
// primary Fetch promotes FallbackClient to primary-only, permanently, for the
// remainder of the process lifetime: it never falls back to the secondary
// again, even if a later primary Fetch fails. This is a deliberate one-way
// ratchet — reverting to a stale local file after Building OS has been
// authoritative risks silently regressing point_id mappings the operator
// believed were long gone. A caller that needs to react to a post-promotion
// primary outage should rely on the normal retry/backoff posture of whatever
// drives Fetch (e.g. pointsync.Loop's poll interval), exactly as it would for
// a plain HTTPClient.
type FallbackClient struct {
	primary   Client
	secondary Client

	mu        sync.Mutex
	promoted  bool
	onPromote func()
}

// NewFallbackClient returns a Client that prefers primary and falls back to
// secondary until primary first succeeds.
func NewFallbackClient(primary, secondary Client) *FallbackClient {
	return &FallbackClient{primary: primary, secondary: secondary}
}

// OnPromote registers a callback invoked exactly once, synchronously within
// Fetch, the moment FallbackClient promotes to primary-only. Intended for
// observability (structured log line, metrics gauge) — see EP-013 FEAT-059.
// Not safe to call concurrently with Fetch; register it before first use.
func (f *FallbackClient) OnPromote(fn func()) *FallbackClient {
	f.onPromote = fn
	return f
}

// Promoted reports whether FallbackClient has switched to primary-only.
func (f *FallbackClient) Promoted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.promoted
}

// Fetch implements Client.
func (f *FallbackClient) Fetch(ctx context.Context, knownETag string) (*FetchResult, error) {
	if f.Promoted() {
		return f.primary.Fetch(ctx, knownETag)
	}

	// Not yet promoted: knownETag belongs to the secondary's ETag namespace, so
	// probe the primary fresh (empty knownETag) rather than passing it through.
	result, err := f.primary.Fetch(ctx, "")
	if err == nil {
		f.promote(result)
		return result, nil
	}

	return f.secondary.Fetch(ctx, knownETag)
}

// promote flips the one-way promoted flag and, if result is non-nil, forces it
// to a full resnapshot: the secondary's delta bookkeeping (if any) does not
// carry over across a source switch.
func (f *FallbackClient) promote(result *FetchResult) {
	f.mu.Lock()
	f.promoted = true
	hook := f.onPromote
	f.mu.Unlock()

	if result != nil {
		result.Full = true
		result.Added = nil
		result.Removed = nil
		result.Changed = nil
	}
	if hook != nil {
		hook()
	}
}
