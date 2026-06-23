// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mockbos

import (
	"log/slog"
	"sync/atomic"

	pb "nexus-gateway/gen"
)

// Server is a minimal mock of gatewaybridge.GatewayIngress for local development.
// It logs every received frame and counts accepted totals.
type Server struct {
	pb.UnimplementedGatewayIngressServer
	accepted atomic.Int64
}

func NewServer() *Server { return &Server{} }

func (s *Server) StreamTelemetry(stream pb.GatewayIngress_StreamTelemetryServer) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			total := s.accepted.Load()
			slog.Info("mock BOS: stream closed", "total_accepted", total)
			return stream.SendAndClose(&pb.StreamAck{Accepted: total})
		}
		n := s.accepted.Add(1)
		slog.Info("mock BOS: frame received",
			"gateway_id", frame.GatewayId,
			"point_id", frame.PointId,
			"value", frame.Value,
			"timestamp", frame.Timestamp,
			"accepted_total", n,
		)
	}
}
