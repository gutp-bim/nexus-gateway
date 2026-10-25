// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/lifecycle"
)

func TestWatchDoesNotRestartClockOwnedConnector(t *testing.T) {
	mock := newMockDocker("clock-owned")
	mock.setInspectRunning(false)
	reg := lifecycle.NewRegistry()
	mgr := lifecycle.NewManager(mock, reg)
	_, err := mgr.Claim(lifecycle.ConnectorSpec{ID: "clock-owned", Image: "local"})
	require.NoError(t, err)
	reg.SetRunning("clock-owned", "clock-owned", true)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { mgr.Watch(ctx, time.Millisecond); close(done) }()
	require.Eventually(t, func() bool { return mock.calls("inspect") > 0 }, time.Second, time.Millisecond)
	cancel()
	<-done
	require.Equal(t, 0, mock.calls("create"))
	require.Equal(t, 0, mock.calls("start"))
}
