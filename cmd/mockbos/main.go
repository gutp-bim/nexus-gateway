// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	pb "nexus-gateway/gen"
	"nexus-gateway/internal/mockbos"
)

func main() {
	addr := flag.String("addr", ":50051", "listen address")
	flag.Parse()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}

	srv := grpc.NewServer()
	pb.RegisterGatewayIngressServer(srv, mockbos.NewServer())

	slog.Info("mock BOS listening", "addr", *addr)
	go func() {
		if err := srv.Serve(lis); err != nil {
			slog.Error("serve error", "err", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	srv.GracefulStop()
	slog.Info("mock BOS stopped")
}
