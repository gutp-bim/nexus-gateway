// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package mockbos

import (
	"errors"
	"io"
	"log/slog"

	pb "nexus-gateway/gen"
)

// EgressServer is a no-op GatewayEgress for the dev stack (#74). The gateway
// always dials GatewayEgress — on BOS_ADDR when no separate egress address is
// configured, which is exactly the default compose wiring — so a mock-bos
// without this service made the egress agent reconnect on Unimplemented
// forever, spamming the gateway log (and the Admin UI Logs screen) with
// warnings. This stub accepts the connection, logs the Hello, consumes
// whatever the gateway sends, and never issues a command.
type EgressServer struct {
	pb.UnimplementedGatewayEgressServer
}

// NewEgressServer returns a no-op GatewayEgress implementation.
func NewEgressServer() *EgressServer {
	return &EgressServer{}
}

// Connect holds the per-gateway stream open until the client closes it or the
// connection drops. Never sends EgressDown — the dev stub has no control plane.
func (s *EgressServer) Connect(stream pb.GatewayEgress_ConnectServer) error {
	for {
		up, err := stream.Recv()
		if err != nil {
			// io.EOF = clean client close; anything else (ctx cancel, transport
			// drop) also just ends this stream — the gateway reconnects.
			if !errors.Is(err, io.EOF) {
				slog.Debug("mockbos: egress stream ended", "err", err)
			}
			return nil
		}
		switch m := up.GetM().(type) {
		case *pb.EgressUp_Hello:
			slog.Info("mockbos: egress connected", "gateway_id", m.Hello.GetGatewayId())
		case *pb.EgressUp_Result:
			slog.Info("mockbos: egress control result (ignored)",
				"control_id", m.Result.GetControlId(), "success", m.Result.GetSuccess())
		default:
			// Unknown/absent oneof — tolerate and keep the stream open.
		}
	}
}
