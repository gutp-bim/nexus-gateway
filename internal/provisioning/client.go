// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"

	"nexus-gateway/internal/pointlist"
)

// FetchResult is the payload returned by a successful Fetch call.
// A nil return (with nil error) means the point list is unchanged (304 Not Modified).
type FetchResult struct {
	ETag string
	// Full is true when Entries holds the complete replacement list.
	// False means only Added/Removed/Changed are populated (delta).
	Full    bool
	Entries []pointlist.Entry // populated when Full == true
	// Delta fields (populated when Full == false):
	Added   []pointlist.Entry
	Removed []string // canonical point_ids to remove from the local copy
	Changed []pointlist.Entry
}

// Client abstracts the Building OS gateway-scoped Point List provisioning API (#224).
type Client interface {
	// Fetch returns nil when knownETag matches the server's current ETag (304 Not Modified).
	// Pass empty knownETag for the initial fetch (always returns a full result).
	Fetch(ctx context.Context, knownETag string) (*FetchResult, error)
}
