// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward

import (
	"context"
	"log/slog"

	pb "nexus-gateway/gen"
)

// Pump reads TelemetryFrames from src and writes them to buf until ctx is done or src is closed.
func Pump(ctx context.Context, src <-chan *pb.TelemetryFrame, buf *Buffer) {
	for {
		select {
		case f, ok := <-src:
			if !ok {
				return
			}
			if err := buf.Write(f); err != nil {
				slog.Warn("storeforward: buffer write error", "err", err)
			}
		case <-ctx.Done():
			return
		}
	}
}
