// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nexus-gateway/internal/testca"
)

// The Point List channel has to reach a Building OS edge that terminates mTLS
// (#135). Until now the provisioning client built a bare http.Client, so it
// could only talk to a server the container already trusted and could never
// present a client certificate — which is what the edge uses to derive the
// gateway id. These tests drive that capability.

const pointListBody = `{"gatewayId":"gw-001","revision":"v1","full":true,
  "points":[{"pointId":"p1","protocol":"bacnet","localId":"analogInput,1"}]}`

// pointListTLSServer starts an HTTPS point-list endpoint. When requireClientCert
// is set the server demands and verifies a client certificate against ca,
// standing in for the mTLS edge. The server certificate carries a "localhost"
// SAN, so callers must dial it by that name.
func pointListTLSServer(t *testing.T, ca *testca.CA, requireClientCert bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The gateway must never assert its own identity here: the trusted edge
		// derives it from the client certificate. A gateway-supplied header would
		// be spoofable, so its absence is part of the contract (#135).
		if got := r.Header.Get("X-Gateway-Id"); got != "" {
			t.Errorf("gateway sent X-Gateway-Id=%q; the edge must derive it from the client cert", got)
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pointListBody))
	}))
	srvCert := ca.IssueServer(t, "localhost")
	tlsCfg := srvCert.ServerTLSConfig(t)
	if requireClientCert {
		tlsCfg.ClientCAs = ca.Pool()
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	srv.TLS = tlsCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func fetchOnce(t *testing.T, c *HTTPClient) (*FetchResult, error) {
	t.Helper()
	return c.Fetch(context.Background(), "")
}

// ── server-only TLS against a private CA ─────────────────────────────────────

func TestHTTPClient_TLS_TrustsSuppliedCA(t *testing.T) {
	ca := testca.New(t)
	srv := pointListTLSServer(t, ca, false)
	dir := t.TempDir()
	caPath := testca.WritePEM(t, dir, "ca.pem", ca.CertPEM())

	c, err := NewHTTPClient(srv.URL, "gw-001", nil, TLSOptions{
		CAFile: caPath, ServerName: "localhost",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	res, err := fetchOnce(t, c)
	if err != nil {
		t.Fatalf("Fetch over TLS with trusted CA: %v", err)
	}
	if res == nil || len(res.Entries) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestHTTPClient_TLS_RejectsUntrustedServer(t *testing.T) {
	srv := pointListTLSServer(t, testca.New(t), false)
	other := testca.New(t) // a CA that did not sign the server
	dir := t.TempDir()
	caPath := testca.WritePEM(t, dir, "ca.pem", other.CertPEM())

	c, err := NewHTTPClient(srv.URL, "gw-001", nil, TLSOptions{
		CAFile: caPath, ServerName: "localhost",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	// Verification must fail rather than fall back to trusting anything.
	if _, err := fetchOnce(t, c); err == nil {
		t.Fatal("Fetch succeeded against a server signed by an untrusted CA")
	}
}

// ── mTLS ─────────────────────────────────────────────────────────────────────

func TestHTTPClient_MTLS_PresentsClientCertificate(t *testing.T) {
	ca := testca.New(t)
	srv := pointListTLSServer(t, ca, true)
	dir := t.TempDir()
	caPath := testca.WritePEM(t, dir, "ca.pem", ca.CertPEM())
	certPath, keyPath := ca.IssueClient(t, "gw-001").WriteFiles(t, dir, "client")

	c, err := NewHTTPClient(srv.URL, "gw-001", nil, TLSOptions{
		CAFile: caPath, CertFile: certPath, KeyFile: keyPath, ServerName: "localhost",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if _, err := fetchOnce(t, c); err != nil {
		t.Fatalf("Fetch against mTLS server: %v", err)
	}
}

func TestHTTPClient_MTLS_RejectedWithoutClientCertificate(t *testing.T) {
	ca := testca.New(t)
	srv := pointListTLSServer(t, ca, true)
	dir := t.TempDir()
	caPath := testca.WritePEM(t, dir, "ca.pem", ca.CertPEM())

	c, err := NewHTTPClient(srv.URL, "gw-001", nil, TLSOptions{
		CAFile: caPath, ServerName: "localhost",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if _, err := fetchOnce(t, c); err == nil {
		t.Fatal("Fetch succeeded without a client certificate against a server that requires one")
	}
}

// ── configuration validation ─────────────────────────────────────────────────

func TestHTTPClient_CertAndKeyMustBeSetTogether(t *testing.T) {
	dir := t.TempDir()
	ca := testca.New(t)
	certPath, keyPath := ca.IssueClient(t, "gw-001").WriteFiles(t, dir, "client")

	for name, opts := range map[string]TLSOptions{
		"cert without key": {CertFile: certPath},
		"key without cert": {KeyFile: keyPath},
	} {
		t.Run(name, func(t *testing.T) {
			// Fail fast at construction: a half-configured client that silently
			// omits the certificate would surface as an opaque 403 from the edge.
			_, err := NewHTTPClient("https://bos.example", "gw-001", nil, opts)
			if err == nil {
				t.Fatal("expected a configuration error, got nil")
			}
			if !strings.Contains(err.Error(), "together") {
				t.Fatalf("error should say both are required, got: %v", err)
			}
		})
	}
}

func TestHTTPClient_InvalidTLSMaterialErrorsWithoutDisablingVerification(t *testing.T) {
	dir := t.TempDir()
	ca := testca.New(t)
	certPath, keyPath := ca.IssueClient(t, "gw-001").WriteFiles(t, dir, "client")
	junk := testca.WritePEM(t, dir, "junk.pem", []byte("not a certificate"))

	for name, opts := range map[string]TLSOptions{
		"missing CA file":  {CAFile: filepath.Join(dir, "absent.pem")},
		"unparseable CA":   {CAFile: junk},
		"unparseable key":  {CertFile: certPath, KeyFile: junk},
		"unparseable cert": {CertFile: junk, KeyFile: keyPath},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewHTTPClient("https://bos.example", "gw-001", nil, opts); err == nil {
				t.Fatal("expected an error for invalid TLS material, got nil")
			}
		})
	}
}

func TestHTTPClient_TLSOptionsOnNonHTTPSURL_Rejected(t *testing.T) {
	dir := t.TempDir()
	caPath := testca.WritePEM(t, dir, "ca.pem", testca.New(t).CertPEM())

	// Silently ignoring the credentials would send the point list — and the
	// gateway's identity claim — in clear while looking configured.
	for name, baseURL := range map[string]string{
		"plain http": "http://bos.example/provisioning",
		"no scheme":  "bos.example/provisioning",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewHTTPClient(baseURL, "gw-001", nil, TLSOptions{CAFile: caPath})
			if err == nil {
				t.Fatal("expected TLS options on a non-HTTPS URL to be rejected, got nil")
			}
			if !strings.Contains(err.Error(), "https") {
				t.Fatalf("error should point at the scheme, got: %v", err)
			}
		})
	}
}

func TestHTTPClient_TLSOptionsWithUnparseableURL_Rejected(t *testing.T) {
	dir := t.TempDir()
	caPath := testca.WritePEM(t, dir, "ca.pem", testca.New(t).CertPEM())

	if _, err := NewHTTPClient("https://%zz", "gw-001", nil, TLSOptions{CAFile: caPath}); err == nil {
		t.Fatal("expected an unparseable base URL to be rejected, got nil")
	}
}

func TestHTTPClient_TLSWithReplacedDefaultTransport_ErrorsInsteadOfPanicking(t *testing.T) {
	dir := t.TempDir()
	caPath := testca.WritePEM(t, dir, "ca.pem", testca.New(t).CertPEM())

	// http.DefaultTransport is a public var; an embedding process may swap in a
	// wrapper. An unchecked type assertion would crash the gateway at startup.
	saved := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = saved })
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	})

	_, err := NewHTTPClient("https://bos.example", "gw-001", nil, TLSOptions{CAFile: caPath})
	if err == nil {
		t.Fatal("expected a construction error when DefaultTransport is not *http.Transport, got nil")
	}
	if !strings.Contains(err.Error(), "DefaultTransport") {
		t.Fatalf("error should name the cause, got: %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ── default behaviour is unchanged ───────────────────────────────────────────

func TestHTTPClient_PlainHTTPStillWorksWithZeroTLSOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(pointListBody))
	}))
	t.Cleanup(srv.Close)

	c, err := NewHTTPClient(srv.URL, "gw-001", nil, TLSOptions{})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	res, err := fetchOnce(t, c)
	if err != nil {
		t.Fatalf("Fetch over plain HTTP: %v", err)
	}
	if res == nil || len(res.Entries) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// A zero TLSOptions must keep verifying ordinary public HTTPS against the system
// roots — "no custom CA" means default trust, never no trust.
func TestHTTPClient_ZeroOptionsKeepsSystemRootVerification(t *testing.T) {
	srv := pointListTLSServer(t, testca.New(t), false) // private CA, not in system roots

	// Dial the name the certificate is actually issued for. httptest's URL is
	// https://127.0.0.1:port and the cert carries only a "localhost" SAN, so
	// against srv.URL the handshake fails on the hostname — and the test would
	// pass without ever exercising trust roots, which is the whole claim here.
	c, err := NewHTTPClient(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), "gw-001", nil, TLSOptions{})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	_, err = fetchOnce(t, c)
	if err == nil {
		t.Fatal("zero TLSOptions accepted a server the system roots do not trust")
	}
	// Pin the reason, not just the failure — but by the OS-independent wrapper,
	// not the underlying x509 error type. On darwin, verification with a nil
	// RootCAs is delegated to Security.framework rather than Go's own chain
	// builder, so the wrapped reason is a platform error string, not
	// x509.UnknownAuthorityError (#144). crypto/tls always wraps a handshake
	// certificate rejection in *tls.CertificateVerificationError before
	// returning it, on every platform (crypto/tls/handshake_client.go), so that
	// type is the portable thing to assert on. UnverifiedCertificates being
	// non-empty rules out passing for an unrelated failure — a certificate was
	// actually presented and rejected on trust.
	var certErr *tls.CertificateVerificationError
	if !errors.As(err, &certErr) {
		t.Fatalf("expected a certificate verification rejection, got %v", err)
	}
	if len(certErr.UnverifiedCertificates) == 0 {
		t.Fatalf("expected the rejected certificate to be attached to the error, got %v", certErr)
	}
}
