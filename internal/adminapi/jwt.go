// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package adminapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// KeyFetcher retrieves the JWKS key set used to verify tokens.
type KeyFetcher func(ctx context.Context) (jwk.Set, error)

// newURLKeyFetcher returns a KeyFetcher backed by a refreshing JWKS cache.
// ctx controls the cache's background refresh goroutine — cancel it (via
// Server.Shutdown) to stop the goroutine cleanly.
func newURLKeyFetcher(ctx context.Context, jwksURL string) KeyFetcher {
	cache := jwk.NewCache(ctx)
	cache.Register(jwksURL, jwk.WithMinRefreshInterval(15*time.Minute))
	// Warm cache immediately so the first request doesn't pay fetch latency.
	if _, err := cache.Refresh(ctx, jwksURL); err != nil {
		slog.Warn("adminapi: initial JWKS fetch failed — tokens will be rejected until next refresh",
			"url", jwksURL, "err", err)
	}
	return func(ctx context.Context) (jwk.Set, error) {
		return cache.Get(ctx, jwksURL)
	}
}

// JWTMiddleware validates Keycloak-issued bearer tokens.
type JWTMiddleware struct {
	keys     KeyFetcher
	audience string
	issuer   string
}

// require returns an http.HandlerFunc that:
//  1. Extracts the Bearer token from Authorization header.
//  2. Validates signature, expiry, audience, and issuer via JWKS.
//  3. Checks that the token carries the required role (or operator implies viewer).
//  4. Calls next on success; writes 401/403 on failure.
func (m *JWTMiddleware) require(role string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractBearer(r)
		if tokenStr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		keySet, err := m.keys(r.Context())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		tok, err := jwt.Parse([]byte(tokenStr),
			jwt.WithKeySet(keySet),
			jwt.WithValidate(true),
			jwt.WithAudience(m.audience),
			jwt.WithIssuer(m.issuer),
		)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		roles := realmRoles(tok)
		if !hasRole(roles, role) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		next(w, r)
	}
}

func extractBearer(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return token
}

// realmRoles extracts roles from the Keycloak realm_access.roles claim.
func realmRoles(tok jwt.Token) []string {
	v, ok := tok.Get("realm_access")
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	rawRoles, ok := m["roles"].([]any)
	if !ok {
		return nil
	}
	roles := make([]string, 0, len(rawRoles))
	for _, r := range rawRoles {
		if s, ok := r.(string); ok {
			roles = append(roles, s)
		}
	}
	return roles
}

// hasRole returns true if required == RoleViewer and the token carries operator,
// or if the token directly carries the required role.
func hasRole(roles []string, required string) bool {
	for _, r := range roles {
		if r == required {
			return true
		}
		if required == RoleViewer && r == RoleOperator {
			return true // operator implicitly satisfies viewer
		}
	}
	return false
}
