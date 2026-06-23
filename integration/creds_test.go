// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// insecureCreds is the plaintext-h2c transport used by the integration tests,
// which dial a local mock Building OS with no TLS. Production wiring builds
// TLS/mTLS credentials via internal/transport (ADR-0007).
func insecureCreds() credentials.TransportCredentials { return insecure.NewCredentials() }
