// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
)

// fakeClient is a scripted provisioning.Client double for fallback tests: Mock
// (in internal/provisioning/mock.go) cannot simulate a Fetch error, which is
// exactly the condition FallbackClient exists to react to.
type fakeClient struct {
	mu    sync.Mutex
	fn    func(ctx context.Context, knownETag string) (*provisioning.FetchResult, error)
	calls []string // knownETag argument of every Fetch call, in order
}

func (f *fakeClient) Fetch(ctx context.Context, knownETag string) (*provisioning.FetchResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, knownETag)
	fn := f.fn
	f.mu.Unlock()
	return fn(ctx, knownETag)
}

func (f *fakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeClient) knownETags() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func alwaysErr(err error) func(context.Context, string) (*provisioning.FetchResult, error) {
	return func(context.Context, string) (*provisioning.FetchResult, error) { return nil, err }
}

func alwaysResult(r *provisioning.FetchResult) func(context.Context, string) (*provisioning.FetchResult, error) {
	return func(context.Context, string) (*provisioning.FetchResult, error) { return r, nil }
}

var errUnreachable = errors.New("dial tcp: connection refused")

func TestFallbackClient_UsesSecondaryWhilePrimaryUnreachable(t *testing.T) {
	primary := &fakeClient{fn: alwaysErr(errUnreachable)}
	secondary := &fakeClient{fn: alwaysResult(&provisioning.FetchResult{
		ETag: "file-etag", Full: true,
		Entries: []pointlist.Entry{{PointID: "p1"}},
	})}
	fb := provisioning.NewFallbackClient(primary, secondary)

	result, err := fb.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "file-etag", result.ETag)
	assert.Equal(t, 1, primary.callCount())
	assert.Equal(t, 1, secondary.callCount())
}

func TestFallbackClient_PrimarySuccessOnFirstCall_NeverTouchesSecondary(t *testing.T) {
	primary := &fakeClient{fn: alwaysResult(&provisioning.FetchResult{
		ETag: "bos-etag", Full: true,
		Entries: []pointlist.Entry{{PointID: "p1"}},
	})}
	secondary := &fakeClient{fn: alwaysErr(errors.New("must not be called"))}
	fb := provisioning.NewFallbackClient(primary, secondary)

	result, err := fb.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "bos-etag", result.ETag)
	assert.Equal(t, 0, secondary.callCount(), "secondary must never be consulted when primary succeeds immediately")
}

func TestFallbackClient_PromotesPermanentlyOnFirstPrimarySuccess(t *testing.T) {
	primaryUp := false
	primary := &fakeClient{fn: func(ctx context.Context, knownETag string) (*provisioning.FetchResult, error) {
		if !primaryUp {
			return nil, errUnreachable
		}
		return &provisioning.FetchResult{ETag: "bos-etag-1", Full: true, Entries: []pointlist.Entry{{PointID: "p1"}}}, nil
	}}
	secondary := &fakeClient{fn: alwaysResult(&provisioning.FetchResult{
		ETag: "file-etag", Full: true, Entries: []pointlist.Entry{{PointID: "file1"}},
	})}
	fb := provisioning.NewFallbackClient(primary, secondary)
	ctx := context.Background()

	// Tick 1: Building OS is down — served from the file.
	r1, err := fb.Fetch(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, "file-etag", r1.ETag)

	// Building OS comes up.
	primaryUp = true

	// Tick 2: promotes to Building OS.
	r2, err := fb.Fetch(ctx, "")
	require.NoError(t, err)
	require.NotNil(t, r2)
	assert.Equal(t, "bos-etag-1", r2.ETag)
	assert.True(t, r2.Full, "promotion must yield a full resnapshot, not a delta")
	assert.Empty(t, r2.Added)
	assert.Empty(t, r2.Removed)
	assert.Empty(t, r2.Changed)

	secondaryCallsAfterPromotion := secondary.callCount()

	// Tick 3: even though Building OS is now (hypothetically) still up, confirm
	// the secondary is never consulted again post-promotion.
	r3, err := fb.Fetch(ctx, "bos-etag-1")
	require.NoError(t, err)
	assert.Equal(t, "bos-etag-1", r3.ETag)
	assert.Equal(t, secondaryCallsAfterPromotion, secondary.callCount(),
		"secondary must not be consulted again after promotion")
}

func TestFallbackClient_NeverFallsBackAfterPromotion(t *testing.T) {
	primaryCall := 0
	primary := &fakeClient{fn: func(ctx context.Context, knownETag string) (*provisioning.FetchResult, error) {
		primaryCall++
		if primaryCall == 1 {
			return &provisioning.FetchResult{ETag: "bos-etag", Full: true}, nil // promotes
		}
		return nil, errUnreachable // BOS drops after promotion
	}}
	secondary := &fakeClient{fn: alwaysResult(&provisioning.FetchResult{ETag: "file-etag", Full: true})}
	fb := provisioning.NewFallbackClient(primary, secondary)
	ctx := context.Background()

	_, err := fb.Fetch(ctx, "") // promotes
	require.NoError(t, err)

	_, err = fb.Fetch(ctx, "bos-etag") // BOS now failing again
	require.Error(t, err, "a post-promotion primary failure must propagate, not fall back to the file")
	assert.Equal(t, 0, secondary.callCount(), "secondary must never be called after promotion")
}

func TestFallbackClient_PostPromotionForwardsRealKnownETag(t *testing.T) {
	primary := &fakeClient{fn: alwaysResult(&provisioning.FetchResult{ETag: "bos-etag", Full: true})}
	secondary := &fakeClient{fn: alwaysResult(&provisioning.FetchResult{ETag: "file-etag", Full: true})}
	fb := provisioning.NewFallbackClient(primary, secondary)
	ctx := context.Background()

	_, err := fb.Fetch(ctx, "whatever-file-etag-the-loop-had") // promotes; must not forward this to primary
	require.NoError(t, err)
	_, err = fb.Fetch(ctx, "bos-etag") // post-promotion: the real HTTP etag must reach primary
	require.NoError(t, err)

	got := primary.knownETags()
	require.Len(t, got, 2)
	assert.Equal(t, "", got[0], "the pre-promotion attempt must not leak the file's ETag namespace to the primary")
	assert.Equal(t, "bos-etag", got[1], "post-promotion calls must forward the caller's real knownETag")
}

func TestFallbackClient_PromoteHookFiresExactlyOnceOnPromotion(t *testing.T) {
	primaryUp := false
	primary := &fakeClient{fn: func(ctx context.Context, knownETag string) (*provisioning.FetchResult, error) {
		if !primaryUp {
			return nil, errUnreachable
		}
		return &provisioning.FetchResult{ETag: "bos-etag", Full: true}, nil
	}}
	secondary := &fakeClient{fn: alwaysResult(&provisioning.FetchResult{ETag: "file-etag", Full: true})}

	var hookCalls int
	fb := provisioning.NewFallbackClient(primary, secondary).OnPromote(func() { hookCalls++ })
	ctx := context.Background()

	_, _ = fb.Fetch(ctx, "") // file
	assert.Equal(t, 0, hookCalls)

	primaryUp = true
	_, _ = fb.Fetch(ctx, "") // promotes
	assert.Equal(t, 1, hookCalls)

	_, _ = fb.Fetch(ctx, "bos-etag") // steady state on primary
	assert.Equal(t, 1, hookCalls, "the promote hook must fire exactly once, not on every subsequent tick")
}

func TestFallbackClient_SecondaryUnchanged_ReturnsNilBeforePromotion(t *testing.T) {
	primary := &fakeClient{fn: alwaysErr(errUnreachable)}
	secondary := &fakeClient{fn: alwaysResult(nil)} // 304: unchanged
	fb := provisioning.NewFallbackClient(primary, secondary)

	result, err := fb.Fetch(context.Background(), "file-etag")
	require.NoError(t, err)
	assert.Nil(t, result, "an unchanged file (304) must propagate as nil, not an empty snapshot")
}

// FallbackClient must satisfy the provisioning.Client interface.
var _ provisioning.Client = (*provisioning.FallbackClient)(nil)
