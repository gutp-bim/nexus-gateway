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
				// Attribute the loss to a specific point/gateway (matching the
				// Normalizer's attribution style) so a persistent write failure is
				// diagnosable, not just a bare error (#25).
				slog.Warn("storeforward: buffer write error",
					"err", err, "point_id", f.PointId, "gateway_id", f.GatewayId)
			}
		case <-ctx.Done():
			return
		}
	}
}
