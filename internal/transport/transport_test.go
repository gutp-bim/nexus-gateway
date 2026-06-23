// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "nexus-gateway/gen"
)

// ── tracer: insecure opt-in returns insecure credentials ──────────────────────

func TestInsecureConfigReturnsInsecureCredentials(t *testing.T) {
	creds, err := ClientCredentials(Config{Insecure: true})
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	if got := creds.Info().SecurityProtocol; got != "insecure" {
		t.Fatalf("SecurityProtocol = %q, want insecure", got)
	}
}

// ── default-secure: a TLS server is reachable when the CA is trusted ───────────

func TestTLSHandshakeSucceedsWithTrustedCA(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
	srvCert := ca.issue(t, "localhost", false)

	addr := startTLSGreeter(t, srvCert, nil) // no client-cert requirement

	creds, err := ClientCredentials(Config{CAFile: caPath, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	if err := dialAndGreet(t, addr, creds); err != nil {
		t.Fatalf("greet over TLS: %v", err)
	}
}

func TestTLSHandshakeFailsWithUntrustedCA(t *testing.T) {
	serverCA := newTestCA(t)
	otherCA := newTestCA(t)
	dir := t.TempDir()
	otherCAPath := writePEM(t, dir, "other-ca.pem", otherCA.certPEM)
	srvCert := serverCA.issue(t, "localhost", false)

	addr := startTLSGreeter(t, srvCert, nil)

	creds, err := ClientCredentials(Config{CAFile: otherCAPath, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	err = dialAndGreet(t, addr, creds)
	if err == nil {
		t.Fatal("expected handshake failure against untrusted CA, got nil")
	}
	// Assert it failed at certificate verification, not for an unrelated reason
	// (refused/timeout) — otherwise a skipped-verification regression would pass.
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("want a certificate-verification error, got: %v", err)
	}
}

func TestTLSHandshakeFailsOnServerNameMismatch(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
	srvCert := ca.issue(t, "localhost", false) // SAN = localhost

	addr := startTLSGreeter(t, srvCert, nil)

	// Trust the CA but verify against the wrong name → SAN mismatch.
	creds, err := ClientCredentials(Config{CAFile: caPath, ServerName: "wrong.example"})
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	err = dialAndGreet(t, addr, creds)
	if err == nil {
		t.Fatal("expected SAN-mismatch failure, got nil")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("want a certificate (hostname) verification error, got: %v", err)
	}
}

// ── mTLS: server requiring a client cert rejects a client without one ──────────

func TestMTLSRequiresClientCert(t *testing.T) {
	ca := newTestCA(t)
	dir := t.TempDir()
	caPath := writePEM(t, dir, "ca.pem", ca.certPEM)
	srvCert := ca.issue(t, "localhost", false)

	addr := startTLSGreeter(t, srvCert, ca.pool()) // require + verify client cert

	// Client without a cert → rejected.
	noCert, err := ClientCredentials(Config{CAFile: caPath, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("ClientCredentials(no client cert): %v", err)
	}
	rejErr := dialAndGreet(t, addr, noCert)
	if rejErr == nil {
		t.Fatal("expected mTLS rejection without client cert, got nil")
	}
	// The server requires a client cert and aborts the handshake; depending on
	// timing the client surfaces this as a TLS alert or a reset/broken-pipe write
	// error — but never as a hang. Assert it was a prompt rejection, not a timeout.
	if strings.Contains(rejErr.Error(), "context deadline") || strings.Contains(rejErr.Error(), "DeadlineExceeded") {
		t.Fatalf("expected a prompt mTLS rejection, got a timeout: %v", rejErr)
	}

	// Client with a CA-issued cert → accepted.
	cli := ca.issue(t, "gw-001", true)
	certPath := writePEM(t, dir, "cli.pem", cli.certPEM)
	keyPath := writePEM(t, dir, "cli-key.pem", cli.keyPEM)
	withCert, err := ClientCredentials(Config{CAFile: caPath, CertFile: certPath, KeyFile: keyPath, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("ClientCredentials(mTLS): %v", err)
	}
	if err := dialAndGreet(t, addr, withCert); err != nil {
		t.Fatalf("mTLS greet with client cert: %v", err)
	}
}

func TestClientCertRequiresBothFiles(t *testing.T) {
	if _, err := ClientCredentials(Config{CertFile: "x.pem"}); err == nil {
		t.Fatal("CertFile without KeyFile should error")
	}
	if _, err := ClientCredentials(Config{KeyFile: "x.pem"}); err == nil {
		t.Fatal("KeyFile without CertFile should error")
	}
}

func TestMissingCAFileErrors(t *testing.T) {
	if _, err := ClientCredentials(Config{CAFile: "/nonexistent/ca.pem"}); err == nil {
		t.Fatal("missing CA file should error")
	}
}

func TestInsecureWithTLSMaterialRejected(t *testing.T) {
	// Insecure must not silently win over supplied TLS material — that would ship
	// cleartext while the operator believes mTLS is configured.
	for _, cfg := range []Config{
		{Insecure: true, CAFile: "ca.pem"},
		{Insecure: true, CertFile: "c.pem", KeyFile: "k.pem"},
		{Insecure: true, ServerName: "bos.example"},
	} {
		if _, err := ClientCredentials(cfg); err == nil {
			t.Fatalf("Insecure + TLS material must error, got nil for %+v", cfg)
		}
	}
}

// ── greeter test server ───────────────────────────────────────────────────────
// A minimal GatewayIngress server. The RPC body is irrelevant; dialAndGreet sends
// one frame and closes so the TLS handshake is actually forced to complete (or
// fail), since grpc.NewClient dials lazily.

type greeter struct {
	pb.UnimplementedGatewayIngressServer
}

func (greeter) StreamTelemetry(stream pb.GatewayIngress_StreamTelemetryServer) error {
	// Drain until client closes; reply with an ack.
	for {
		if _, err := stream.Recv(); err != nil {
			return stream.SendAndClose(&pb.StreamAck{})
		}
	}
}

func startTLSGreeter(t *testing.T, srvCert tlsCert, clientCAs *x509.CertPool) string {
	t.Helper()
	tlsCfg := srvCert.serverTLSConfig(t)
	if clientCAs != nil {
		tlsCfg.ClientCAs = clientCAs
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	pb.RegisterGatewayIngressServer(srv, greeter{})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func dialAndGreet(t *testing.T, addr string, creds credentials.TransportCredentials) error {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := pb.NewGatewayIngressClient(conn).StreamTelemetry(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.TelemetryFrame{GatewayId: "gw-001", PointId: "p"}); err != nil {
		return err
	}
	_, err = stream.CloseAndRecv()
	return err
}

// ── tiny self-signed CA helper ────────────────────────────────────────────────

type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

type tlsCert struct {
	certPEM []byte
	keyPEM  []byte
}

func newTestCA(t *testing.T) *testCA {
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
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

func (ca *testCA) issue(t *testing.T, cn string, client bool) tlsCert {
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
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	return tlsCert{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
}

func (c tlsCert) serverTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	pair, err := tls.X509KeyPair(c.certPEM, c.keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
}

func writePEM(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
