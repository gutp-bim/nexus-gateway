// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/testca"
)

func clockHTTPMock(t *testing.T, handler http.HandlerFunc) (*ClockHTTPClient, *httptest.Server) {
	t.Helper()
	ca := testca.New(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/gateways/GW-EXAMPLE/provisioning/v1", r.URL.Path)
		require.Empty(t, r.URL.RawQuery)
		require.Empty(t, r.Header.Get("X-Gateway-Id"))
		require.Empty(t, r.Header.Get("X-Forwarded-Client-Cert"))
		require.Empty(t, r.Header.Get("Authorization"))
		require.NotEmpty(t, r.TLS.PeerCertificates)
		require.Equal(t, "GW-EXAMPLE", r.TLS.PeerCertificates[0].Subject.CommonName)
		handler(w, r)
	}))
	srv.TLS = ca.IssueServer(t, "localhost").ServerTLSConfig(t)
	srv.TLS.ClientCAs = ca.Pool()
	srv.TLS.ClientAuth = tls.RequireAndVerifyClientCert
	srv.StartTLS()
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	cert, key := ca.IssueClient(t, "GW-EXAMPLE").WriteFiles(t, dir, "client")
	c, err := NewClockHTTPClient(srv.URL, ClockScope{"GW-EXAMPLE", "bacnet-example", 1234}, ClockHTTPOptions{
		TLS: TLSOptions{CAFile: testca.WritePEM(t, dir, "ca.pem", ca.CertPEM()), CertFile: cert, KeyFile: key, ServerName: "localhost"}, Timeout: time.Second, Attempts: 3, RetryDelay: time.Millisecond,
	})
	require.NoError(t, err)
	return c, srv
}

func TestClockHTTPFullSnapshotAnd304(t *testing.T) {
	data := clockFixture(t)
	scope := ClockScope{"GW-EXAMPLE", "bacnet-example", 1234}
	s, err := StageClock(data, scope)
	require.NoError(t, err)
	var calls atomic.Int32
	c, _ := clockHTTPMock(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("ETag", s.Revision())
		if r.Header.Get("If-None-Match") == s.Revision() {
			w.WriteHeader(304)
			return
		}
		_, _ = w.Write(data)
	})
	got, err := c.Fetch(t.Context(), "")
	require.NoError(t, err)
	require.Equal(t, data, got.Bytes())
	unchanged, err := c.Fetch(t.Context(), got.Revision())
	require.NoError(t, err)
	require.Nil(t, unchanged)
	require.EqualValues(t, 2, calls.Load())
	native := got.Native()
	native.Points[0].Unit = "tampered"
	native.Clocks[0].PointLocalIDs[0] = "tampered"
	entries := got.Entries()
	entries[0].PointID = "tampered"
	raw := got.Bytes()
	raw[0] = '!'
	require.Equal(t, "1", got.Native().Points[0].Unit)
	require.NotEqual(t, "tampered", got.Native().Clocks[0].PointLocalIDs[0])
	require.Equal(t, "BPT-0002", got.Entries()[0].PointID)
	require.Equal(t, data, got.Bytes())
}

func TestClockHTTPRetriesTransientOnly(t *testing.T) {
	s, err := StageClock(clockFixture(t), ClockScope{"GW-EXAMPLE", "bacnet-example", 1234})
	require.NoError(t, err)
	for _, status := range []int{429, 500, 503, 403, 404, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			c, _ := clockHTTPMock(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				w.Header().Set("ETag", s.Revision())
				_, _ = w.Write(s.Bytes())
			})
			got, err := c.Fetch(t.Context(), "")
			if status == 429 || status >= 500 {
				require.NoError(t, err)
				require.NotNil(t, got)
				require.EqualValues(t, 2, calls.Load())
			} else {
				require.Error(t, err)
				require.Nil(t, got)
				require.EqualValues(t, 1, calls.Load())
			}
		})
	}
}

func TestClockHTTPRejectsProtocolAndSnapshotErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		etag   string
		body   []byte
	}{
		{"initial 304", 304, `"unknown"`, nil},
		{"missing ETag", 200, "", clockFixture(t)},
		{"mismatched ETag", 200, `"other"`, clockFixture(t)},
		{"malformed body", 200, `"bad"`, []byte(`{`)},
		{"invalid revision", 200, `"bad"`, mutateClockFixture(t, func(b map[string]any) { b["revision"] = "bad" }, false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			c, _ := clockHTTPMock(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("ETag", test.etag)
				w.WriteHeader(test.status)
				_, _ = w.Write(test.body)
			})
			s, err := c.Fetch(t.Context(), "")
			require.Error(t, err)
			require.Nil(t, s)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestClockHTTPTimeoutCancelAndRedirect(t *testing.T) {
	var calls atomic.Int32
	c, _ := clockHTTPMock(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() })
	c.opts.Timeout = 30 * time.Millisecond
	c.opts.Attempts = 2
	start := time.Now()
	s, err := c.Fetch(t.Context(), "")
	require.Error(t, err)
	require.Nil(t, s)
	require.Less(t, time.Since(start), time.Second)
	require.EqualValues(t, 2, calls.Load())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = c.Fetch(ctx, "")
	require.Error(t, err)
	calls.Store(0)
	c, _ = clockHTTPMock(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "https://example.invalid/", http.StatusFound)
	})
	_, err = c.Fetch(t.Context(), "")
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load(), "redirect is permanent, not followed/retried")
}

func TestClockHTTPRejectsUntrustedCAWithoutRetry(t *testing.T) {
	c, _ := clockHTTPMock(t, func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS must not reach handler") })
	c.http.Transport.(*http.Transport).TLSClientConfig.RootCAs = testca.New(t).Pool()
	s, retry, err := c.fetch(t.Context(), "")
	require.Error(t, err)
	require.Nil(t, s)
	require.False(t, retry)
}

func TestClockHTTPBodyTimeout(t *testing.T) {
	var calls atomic.Int32
	c, _ := clockHTTPMock(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	c.opts.Timeout = 30 * time.Millisecond
	c.opts.Attempts = 2
	s, err := c.Fetch(t.Context(), "")
	require.Error(t, err)
	require.Nil(t, s)
	require.EqualValues(t, 2, calls.Load())
}

func TestClockHTTPRequiresAuthenticatedTLS(t *testing.T) {
	for _, url := range []string{"http://localhost", "https://user:secret@localhost", "https://localhost?x=y", "https://localhost"} {
		_, err := NewClockHTTPClient(url, ClockScope{"GW-EXAMPLE", "bacnet-example", 1234}, ClockHTTPOptions{})
		require.Error(t, err)
	}
}
