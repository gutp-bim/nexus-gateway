// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package adminapi_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/adminapi"
)

// The bundled dev realm (fixtures/keycloak/realm.json) backs the two
// authentication paths the quickstart documents: the password-grant token
// command (getting-started) and browser sign-in to the Admin UI on the
// compose-published port. These tests pin the realm config that makes both
// work, so a fixture edit that silently reintroduces `unauthorized_client`
// (direct grants off) or `Invalid redirect_uri` (wrong UI origin) fails here
// instead of only surfacing in a live `docker compose up` (#18).

type realmFixture struct {
	Realm   string `json:"realm"`
	Clients []struct {
		ClientID                  string   `json:"clientId"`
		DirectAccessGrantsEnabled bool     `json:"directAccessGrantsEnabled"`
		RedirectURIs              []string `json:"redirectUris"`
		WebOrigins                []string `json:"webOrigins"`
	} `json:"clients"`
	Users []struct {
		Username   string   `json:"username"`
		RealmRoles []string `json:"realmRoles"`
	} `json:"users"`
}

// composeUIOrigin is the Admin UI origin as published by docker-compose.yml
// (host port 13000). Browser sign-in redirects here, so it must be a
// registered redirect URI / web origin in the realm.
const composeUIOrigin = "http://localhost:13000"

func loadRealmFixture(t *testing.T) realmFixture {
	t.Helper()
	// internal/adminapi → repo root is two levels up.
	path := filepath.Join("..", "..", "fixtures", "keycloak", "realm.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read realm fixture")
	var rf realmFixture
	require.NoError(t, json.Unmarshal(raw, &rf), "parse realm fixture")
	return rf
}

func adminUIClient(t *testing.T, rf realmFixture) struct {
	ClientID                  string   `json:"clientId"`
	DirectAccessGrantsEnabled bool     `json:"directAccessGrantsEnabled"`
	RedirectURIs              []string `json:"redirectUris"`
	WebOrigins                []string `json:"webOrigins"`
} {
	t.Helper()
	for _, c := range rf.Clients {
		if c.ClientID == "admin-ui" {
			return c
		}
	}
	t.Fatal("admin-ui client not found in realm fixture")
	return rf.Clients[0] // unreachable
}

// TestRealmFixture_DirectGrantsEnabled guards the quickstart password-grant
// token command: without direct access grants the token request returns
// unauthorized_client.
func TestRealmFixture_DirectGrantsEnabled(t *testing.T) {
	c := adminUIClient(t, loadRealmFixture(t))
	assert.True(t, c.DirectAccessGrantsEnabled,
		"admin-ui client must enable direct access grants so the quickstart password-grant token command works")
}

// TestRealmFixture_RegistersComposeUIOrigin guards browser sign-in: the
// compose-published UI origin (port 13000) must be a registered redirect URI
// and web origin, else Keycloak rejects the login with Invalid redirect_uri.
func TestRealmFixture_RegistersComposeUIOrigin(t *testing.T) {
	c := adminUIClient(t, loadRealmFixture(t))

	hasRedirect := slices.ContainsFunc(c.RedirectURIs, func(u string) bool {
		return u == composeUIOrigin+"/*"
	})
	assert.True(t, hasRedirect,
		"redirectUris must include %s/* for compose-published Admin UI login; got %v", composeUIOrigin, c.RedirectURIs)

	assert.Contains(t, c.WebOrigins, composeUIOrigin,
		"webOrigins must include the compose-published UI origin")
}

// TestRealmFixture_UserRolesMatchMiddleware ensures the seeded users carry the
// exact realm roles the Admin API middleware checks, so the authenticated
// quickstart curl examples are actually authorized.
func TestRealmFixture_UserRolesMatchMiddleware(t *testing.T) {
	rf := loadRealmFixture(t)
	roles := map[string][]string{}
	for _, u := range rf.Users {
		roles[u.Username] = u.RealmRoles
	}

	require.Contains(t, roles, "operator", "operator user must exist for the quickstart")
	assert.Contains(t, roles["operator"], adminapi.RoleOperator,
		"operator user must carry the operator role the middleware requires")

	require.Contains(t, roles, "viewer", "viewer user must exist for the quickstart")
	assert.Contains(t, roles["viewer"], adminapi.RoleViewer,
		"viewer user must carry the viewer role the middleware requires")
}
