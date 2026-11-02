// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nexus-gateway/internal/pointlist"
)

// ClockScope deliberately limits this opt-in API to the supported single
// BACnet connector/device. It does not replace the legacy Point List client.
type ClockScope struct {
	GatewayID   string
	ConnectorID string
	DeviceID    int
}

// ClockSnapshot owns an immutable, validated full snapshot. All accessors copy
// mutable data so lifecycle adapters cannot corrupt a cached/rollback candidate.
type ClockSnapshot struct {
	raw     []byte
	native  NativeClockConfig
	entries []pointlist.Entry
}

func StageClock(data []byte, scope ClockScope) (*ClockSnapshot, error) {
	native, entries, err := StageClockSnapshot(data, scope.GatewayID, scope.ConnectorID, scope.DeviceID)
	if err != nil {
		return nil, err
	}
	return &ClockSnapshot{raw: append([]byte(nil), data...), native: *native, entries: entries}, nil
}
func (s *ClockSnapshot) Revision() string { return s.native.Revision }
func (s *ClockSnapshot) Bytes() []byte    { return append([]byte(nil), s.raw...) }
func (s *ClockSnapshot) Entries() []pointlist.Entry {
	return append([]pointlist.Entry(nil), s.entries...)
}
func (s *ClockSnapshot) Native() NativeClockConfig {
	n := s.native
	n.Points = append(make([]NativeClockPoint, 0, len(n.Points)), n.Points...)
	n.Clocks = append(make([]NativeClock, 0, len(n.Clocks)), n.Clocks...)
	for i := range n.Clocks {
		n.Clocks[i].PointLocalIDs = append([]string(nil), n.Clocks[i].PointLocalIDs...)
	}
	return n
}

type ClockHTTPOptions struct {
	TLS        TLSOptions
	Timeout    time.Duration // per attempt, includes reading and validating the body
	Attempts   int
	RetryDelay time.Duration
}

// ClockHTTPClient fetches only full provisioning/v1 snapshots. No trust header,
// JWT impersonation, TLS bypass, legacy delta, or transparent redirect is used.
type ClockHTTPClient struct {
	http     *http.Client
	endpoint string
	scope    ClockScope
	opts     ClockHTTPOptions
}

var errClockRedirect = errors.New("clock provisioning: redirects refused")

func NewClockHTTPClient(baseURL string, scope ClockScope, opts ClockHTTPOptions) (*ClockHTTPClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("clock provisioning: HTTPS base URL without credentials/query/fragment required")
	}
	if strings.TrimSpace(scope.GatewayID) != scope.GatewayID || scope.GatewayID == "" || strings.ContainsAny(scope.GatewayID, "/\\\r\n\t") || scope.ConnectorID == "" || scope.DeviceID < 0 || scope.DeviceID > 4194302 {
		return nil, fmt.Errorf("clock provisioning: invalid scope")
	}
	if opts.TLS.CertFile == "" || opts.TLS.KeyFile == "" {
		return nil, fmt.Errorf("clock provisioning: gateway mTLS certificate and key required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.Attempts == 0 {
		opts.Attempts = 3
	}
	if opts.RetryDelay == 0 {
		opts.RetryDelay = 250 * time.Millisecond
	}
	if opts.Timeout <= 0 || opts.Attempts < 1 || opts.Attempts > 5 || opts.RetryDelay < 0 || opts.RetryDelay > 5*time.Second {
		return nil, fmt.Errorf("clock provisioning: invalid timeout/retry bounds")
	}
	// Reuse exactly the existing provisioning TLS CA/client-cert/key/server-name
	// implementation, without modifying any legacy HTTP/file/fallback behavior.
	legacy, err := NewHTTPClient(baseURL, scope.GatewayID, nil, opts.TLS)
	if err != nil {
		return nil, err
	}
	legacy.http.CheckRedirect = func(*http.Request, []*http.Request) error { return errClockRedirect }
	return &ClockHTTPClient{http: legacy.http, endpoint: strings.TrimRight(baseURL, "/") + "/gateways/" + url.PathEscape(scope.GatewayID) + "/provisioning/v1", scope: scope, opts: opts}, nil
}

// Fetch returns nil only for a well-formed 304 to a nonempty known revision.
// Callers must keep the staged candidate associated with that ETag: fetched is
// not synonymous with applied. Permanent HTTP/validation errors are not retried.
func (c *ClockHTTPClient) Fetch(ctx context.Context, known string) (*ClockSnapshot, error) {
	for attempt := 0; ; attempt++ {
		snapshot, retry, err := c.fetch(ctx, known)
		if err == nil || !retry || attempt+1 >= c.opts.Attempts || ctx.Err() != nil {
			return snapshot, err
		}
		delay := c.opts.RetryDelay * time.Duration(1<<attempt)
		if delay > 2*time.Second {
			delay = 2 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *ClockHTTPClient) fetch(ctx context.Context, known string) (*ClockSnapshot, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	if known != "" {
		req.Header.Set("If-None-Match", known)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, true, ctx.Err()
		}
		var verification *tls.CertificateVerificationError
		if errors.Is(err, errClockRedirect) || errors.As(err, &verification) {
			return nil, false, fmt.Errorf("clock provisioning: TLS verification or redirect refused")
		}
		return nil, true, fmt.Errorf("clock provisioning: transport failure")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if known == "" || resp.Header.Get("ETag") != known {
			return nil, false, fmt.Errorf("clock provisioning: unexpected 304 or ETag")
		}
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode == 429 || (resp.StatusCode >= 500 && resp.StatusCode <= 599), fmt.Errorf("clock provisioning: HTTP %d", resp.StatusCode)
	}
	const limit = 16 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, true, fmt.Errorf("clock provisioning: body read failed")
	}
	if len(data) > limit {
		return nil, false, fmt.Errorf("clock provisioning: snapshot exceeds size limit")
	}
	s, err := StageClock(data, c.scope)
	if err != nil {
		return nil, false, err
	}
	if ctx.Err() != nil {
		return nil, true, ctx.Err()
	}
	if resp.Header.Get("ETag") != s.Revision() {
		return nil, false, fmt.Errorf("clock provisioning: ETag/body revision mismatch")
	}
	return s, false, nil
}
