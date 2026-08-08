// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package adminapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/adminapi"
)

// The container healthcheck in docker-compose.yml is an API contract, but nothing
// enforced it: the route and the healthcheck were introduced together, then a
// stale image was reused and answered 404 on /health/live for 12+ hours of a 24h
// soak while Docker reported the container unhealthy (#120).
//
// Rather than hardcoding the path (which would drift the same way), this reads it
// straight out of docker-compose.yml and asserts the server actually serves it.
// Change the compose healthcheck to a route the gateway does not have and this
// test fails — in `go test`, with no Docker required.
func TestHealthcheckContract_ComposeProbeIsServed(t *testing.T) {
	path, matcher := composeHealthcheckProbe(t)

	srv := httptest.NewServer(adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"docker-compose healthcheck probes %s, but the gateway does not serve it", path)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	// The healthcheck does not merely require 200 — it greps the payload.
	assert.Equal(t, "ok", body["status"],
		"healthcheck greps %q out of %s; the payload no longer satisfies it", matcher, path)
}

// The liveness payload must identify the running build, so a stale image is
// diagnosable from the process itself rather than only from the registry (#120).
func TestHealthcheckContract_LivenessIdentifiesTheBuild(t *testing.T) {
	srv := httptest.NewServer(adminapi.NewServer(&mockManager{}, &mockMonitor{}, adminapi.ServerOptions{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/live")
	require.NoError(t, err)
	defer resp.Body.Close()

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	assert.Equal(t, "ok", body["status"])
	assert.NotEmpty(t, body["version"], "liveness must carry the build version")
	// revision is absent for a non-VCS build (`go test` on a source tree without
	// git metadata), so only its presence-when-set is contractual.
}

// A Windows checkout with core.autocrlf=true is a normal checkout, not an exotic one,
// and it made this test fail with a message that blamed the compose file. Parsing must
// not depend on the operating system's line-ending convention.
//
// The expectation is derived from parsing the real file rather than hardcoded, so the
// two paths cannot drift apart when the compose healthcheck changes.
func TestHealthcheckContract_ParsesCRLFCompose(t *testing.T) {
	raw, err := os.ReadFile("../../docker-compose.yml")
	require.NoError(t, err)

	wantPath, wantMatcher := parseHealthcheckProbe(t, string(raw))

	crlf := strings.ReplaceAll(normalizeNewlines(string(raw)), "\n", "\r\n")
	require.Contains(t, crlf, "\r\n", "the fixture must actually use CRLF or this proves nothing")

	gotPath, gotMatcher := parseHealthcheckProbe(t, crlf)

	assert.Equal(t, wantPath, gotPath, "CRLF checkout must yield the same probe path")
	assert.Equal(t, wantMatcher, gotMatcher)
	assert.NotContains(t, gotPath, "\r", "the probe path must not carry a stray carriage return")
}

// composeHealthcheckProbe extracts the URL path the *gateway* service's
// healthcheck polls, plus the literal it greps for, from docker-compose.yml.
//
// Scoping to the gateway's own service block matters: several services define a
// healthcheck (NATS probes /healthz on its monitoring port), so a file-wide
// search picks the wrong one.
func composeHealthcheckProbe(t *testing.T) (path, matcher string) {
	t.Helper()

	raw, err := os.ReadFile("../../docker-compose.yml")
	require.NoError(t, err, "docker-compose.yml must be readable to verify the healthcheck contract")

	return parseHealthcheckProbe(t, string(raw))
}

// normalizeNewlines makes line-wise parsing independent of the checkout's line-ending
// convention. Without it, a Windows checkout with core.autocrlf=true (the Git for
// Windows default) yields "  gateway:\r", which no exact line comparison below matches
// — the contract test would fail on the developer's machine while passing in CI, and
// the message ("service \"gateway\" not found") points at the compose file rather than
// at the line endings. The repository has no .gitattributes forcing LF, so this cannot
// be assumed away.
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// parseHealthcheckProbe is the parsing half, split out from the file read so it can be
// exercised against synthetic input (see the CRLF regression test).
func parseHealthcheckProbe(t *testing.T, compose string) (path, matcher string) {
	t.Helper()

	// Normalize before any line splitting — every comparison below is exact.
	compose = normalizeNewlines(compose)

	// Narrow twice: to the gateway service, then to its healthcheck. The service
	// block also carries env vars with URLs (the Keycloak issuer), so the probe has
	// to come from the healthcheck itself.
	block := nestedBlock(t, serviceBlock(t, compose, "gateway"), "    healthcheck:")

	// The healthcheck is a shell one-liner, e.g.
	//   wget -qO- http://localhost:8080/health/live | grep -q '"status":"ok"'
	m := regexp.MustCompile(`http://localhost:\d+(/[^\s"']*)`).FindStringSubmatch(block)
	require.NotNil(t, m, "no probe URL found in the gateway healthcheck")
	path = m[1]

	// The probe is a double-quoted YAML scalar, so its inner quotes arrive escaped.
	matcher = `"status":"ok"`
	require.Contains(t, strings.ReplaceAll(block, `\"`, `"`), matcher,
		"the gateway healthcheck no longer greps %s; update this contract test to match", matcher)
	return path, matcher
}

// nestedBlock returns the lines under an exact key line, up to the next line at
// the same or shallower indent.
func nestedBlock(t *testing.T, block, key string) string {
	t.Helper()

	lines := strings.Split(block, "\n")
	indent := len(key) - len(strings.TrimLeft(key, " "))
	start := -1
	for i, l := range lines {
		if l == key {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "key %q not found", strings.TrimSpace(key))

	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if len(l)-len(strings.TrimLeft(l, " ")) <= indent {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// serviceBlock returns the lines of one compose service: from `  <name>:` up to
// the next key at the same indent.
func serviceBlock(t *testing.T, compose, name string) string {
	t.Helper()

	lines := strings.Split(compose, "\n")
	start := -1
	for i, l := range lines {
		if l == "  "+name+":" {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "service %q not found in docker-compose.yml", name)

	for i := start + 1; i < len(lines); i++ {
		l := lines[i]
		// A sibling service starts at the same two-space indent with a non-space
		// third character; blank and deeper-indented lines belong to this block.
		if strings.HasPrefix(l, "  ") && len(l) > 2 && l[2] != ' ' {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}
