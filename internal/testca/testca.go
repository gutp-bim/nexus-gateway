// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package testca issues short-lived certificates for tests that need a real TLS
// handshake rather than a stubbed one.
//
// It exists because the gateway now speaks mTLS to Building OS on two
// independent channels — gRPC telemetry/control (internal/transport) and the
// HTTP Point List provisioning client (internal/provisioning, #135) — and both
// have to be tested against a server that actually demands a client
// certificate. Keeping one issuer means the two channels are exercised against
// identical certificate material, so a divergence between them is a real
// difference in the code under test and not an artefact of two hand-rolled
// fixtures drifting apart.
//
// Test-only: certificates are valid for an hour, use a fixed-purpose key, and
// carry no revocation. Never wire this into a binary.
package testca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CA is a self-signed authority that issues server and client certificates.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

// Cert is an issued leaf certificate and its private key, both PEM-encoded.
type Cert struct {
	CertPEM []byte
	KeyPEM  []byte
}

// New creates a self-signed CA.
func New(t *testing.T) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// CertPEM returns the CA certificate, for writing out as a trust bundle.
func (ca *CA) CertPEM() []byte { return ca.certPEM }

// Pool returns a cert pool trusting only this CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// IssueServer issues a server certificate valid for the given DNS name.
func (ca *CA) IssueServer(t *testing.T, dnsName string) Cert {
	t.Helper()
	return ca.issue(t, dnsName, false)
}

// IssueClient issues a client certificate with cn as its common name. Building
// OS derives the gateway id from this field, so tests that care about identity
// set it to the gateway id.
func (ca *CA) IssueClient(t *testing.T, cn string) Cert {
	t.Helper()
	return ca.issue(t, cn, true)
}

func (ca *CA) issue(t *testing.T, cn string, client bool) Cert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if client {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.DNSNames = []string{cn}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return Cert{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
}

// ServerTLSConfig builds a server-side tls.Config presenting this certificate.
func (c Cert) ServerTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	pair, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
}

// WriteFiles writes the certificate and key into dir and returns their paths,
// in the form the gateway's *_CERT_FILE / *_KEY_FILE settings expect.
func (c Cert) WriteFiles(t *testing.T, dir, prefix string) (certPath, keyPath string) {
	t.Helper()
	return WritePEM(t, dir, prefix+".pem", c.CertPEM), WritePEM(t, dir, prefix+"-key.pem", c.KeyPEM)
}

// WritePEM writes data to dir/name and returns the path.
func WritePEM(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
