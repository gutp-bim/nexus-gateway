// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package e2e holds end-to-end tests that exercise the gateway against running
// simulators and/or a real Building OS stack. They are guarded by the `e2e`
// build tag so they never run in the normal unit suite (`go test ./...`), which
// must stay hermetic.
//
// These tests are SCAFFOLDS authored for EP-009/EP-010 (issues #39, #40, #42,
// #43, #44, #45). The flow under test runs in containers (the BACnet/OPC-UA
// connectors are Python/Java), so each test acts as a NATS/Building-OS client
// that observes the pipeline once the compose stack is up. A test t.Skip()s
// unless its required endpoints are provided via environment variables, so the
// package compiles and is runnable piecemeal as the environment becomes
// available.
//
// Run (with the relevant stack already up):
//
//	# OPC-UA telemetry (#39) — docker compose ... --profile opcua up
//	E2E_NATS_URL=nats://localhost:4222 go test -tags e2e ./test/e2e -run OPCUATelemetry -v
//
//	# BACnet telemetry (#40) — needs host networking for Who-Is broadcast
//	E2E_NATS_URL=nats://localhost:4222 go test -tags e2e ./test/e2e -run BACnetTelemetry -v
//
//	# Control round-trip (#42)
//	E2E_NATS_URL=nats://localhost:4222 go test -tags e2e ./test/e2e -run ControlRoundTrip -v
//
//	# SoS against real Building OS (#43/#44/#45)
//	E2E_BOS_API_URL=http://localhost:5000 ... go test -tags e2e ./test/e2e -run SoS -v
package e2e
