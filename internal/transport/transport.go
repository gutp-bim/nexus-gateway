// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package transport builds gRPC client transport credentials for the gateway's
// links to Building OS (telemetry Ingress uplink and control Egress agent).
//
// Per ADR-0007 the production model is mTLS terminated at the Building OS Envoy
// edge: the gateway presents a client certificate whose CN/SAN encodes its
// gateway_id, and verifies the server against a trusted CA. Plaintext h2c is
// available only as an explicit opt-in for dev/CI where no edge proxy exists.
package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Config selects the transport security for a gRPC client link.
//
// Default-secure: a zero Config (all fields empty/false) yields TLS that
// verifies the server against the system root CAs. Insecure must be opted into
// explicitly.
type Config struct {
	// Insecure uses plaintext h2c (no TLS). Explicit dev/CI opt-in only.
	Insecure bool
	// CAFile is a PEM bundle used to verify the server certificate.
	// Empty uses the host's system root CAs.
	CAFile string
	// CertFile and KeyFile are the client certificate/key for mTLS.
	// Both must be set together, or neither.
	CertFile string
	KeyFile  string
	// ServerName overrides the name checked against the server certificate
	// (useful when dialing by IP or through an edge proxy).
	ServerName string
}

// ClientCredentials builds the gRPC transport credentials described by cfg.
func ClientCredentials(cfg Config) (credentials.TransportCredentials, error) {
	if cfg.Insecure {
		// Refuse a contradictory config rather than silently dropping to plaintext
		// while TLS material is present — that would ship cleartext believing it was mTLS.
		if cfg.CAFile != "" || cfg.CertFile != "" || cfg.KeyFile != "" || cfg.ServerName != "" {
			return nil, fmt.Errorf("transport: Insecure set together with TLS material (CA/cert/key/serverName)")
		}
		return insecure.NewCredentials(), nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("transport: read CA file %q: %w", cfg.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("transport: no certificates parsed from CA file %q", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}

	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return nil, fmt.Errorf("transport: CertFile and KeyFile must be set together")
	}
	if cfg.CertFile != "" {
		pair, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("transport: load client cert: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{pair}
	}

	if cfg.ServerName != "" {
		tlsCfg.ServerName = cfg.ServerName
	}

	return credentials.NewTLS(tlsCfg), nil
}
