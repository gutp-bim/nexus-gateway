// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package catalog_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/catalog"
)

const (
	allowedRegistry = "ghcr.io/myorg"
	gatewayVersion  = "1.0.0"
	validDigest     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	validImage      = "ghcr.io/myorg/opcua-connector"
)

func validManifest() catalog.Manifest {
	return catalog.Manifest{
		Name:              "opcua-connector",
		Version:           "1.2.3",
		Image:             validImage,
		Digest:            validDigest,
		MinGatewayVersion: "1.0.0",
		Permissions: catalog.Permissions{
			Network: []string{"opc.tcp"},
			Mounts:  []string{"/var/lib/opcua"},
		},
		SignatureRequired: true,
	}
}

// ── Manifest.Validate ────────────────────────────────────────────────────────

func TestManifest_ValidManifestPasses(t *testing.T) {
	m := validManifest()
	err := m.Validate([]string{allowedRegistry}, gatewayVersion)
	require.NoError(t, err)
}

func TestManifest_TagOnlyRejected(t *testing.T) {
	m := validManifest()
	m.Digest = "" // no digest
	err := m.Validate([]string{allowedRegistry}, gatewayVersion)
	require.ErrorIs(t, err, catalog.ErrNoDigest)
}

func TestManifest_MalformedDigestRejected(t *testing.T) {
	m := validManifest()
	m.Digest = "sha256:tooshort"
	err := m.Validate([]string{allowedRegistry}, gatewayVersion)
	require.ErrorIs(t, err, catalog.ErrInvalidDigest)
}

func TestManifest_NonAllowlistedRegistryRejected(t *testing.T) {
	m := validManifest()
	m.Image = "docker.io/malicious/img"
	err := m.Validate([]string{allowedRegistry}, gatewayVersion)
	require.ErrorIs(t, err, catalog.ErrRegistryNotAllowed)
}

func TestManifest_MinVersionTooNewRejected(t *testing.T) {
	m := validManifest()
	m.MinGatewayVersion = "2.0.0"
	err := m.Validate([]string{allowedRegistry}, "1.0.0")
	require.ErrorIs(t, err, catalog.ErrVersionTooOld)
}

func TestManifest_MinVersionEqualAccepted(t *testing.T) {
	m := validManifest()
	m.MinGatewayVersion = "1.0.0"
	err := m.Validate([]string{allowedRegistry}, "1.0.0")
	require.NoError(t, err)
}

func TestManifest_ImageRefIsDigestPinned(t *testing.T) {
	m := validManifest()
	ref := m.ImageRef()
	assert.Equal(t, validImage+"@"+validDigest, ref)
}

// ── FileClient ───────────────────────────────────────────────────────────────

func TestFileClient_FetchManifest(t *testing.T) {
	m := validManifest()
	data, err := json.Marshal([]catalog.Manifest{m})
	require.NoError(t, err)

	dir := t.TempDir()
	f := filepath.Join(dir, "catalog.json")
	require.NoError(t, os.WriteFile(f, data, 0o600))

	client := catalog.NewFileClient(f)
	got, err := client.Fetch(context.Background(), "opcua-connector")
	require.NoError(t, err)
	assert.Equal(t, m, got)
}

func TestFileClient_NotFound(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "catalog.json")
	require.NoError(t, os.WriteFile(f, []byte("[]"), 0o600))

	client := catalog.NewFileClient(f)
	_, err := client.Fetch(context.Background(), "nonexistent")
	require.ErrorIs(t, err, catalog.ErrManifestNotFound)
}

// ── NoopVerifier ─────────────────────────────────────────────────────────────

func TestNoopVerifier_AlwaysSucceeds(t *testing.T) {
	v := catalog.NoopVerifier{}
	err := v.Verify(context.Background(), validImage+"@"+validDigest)
	require.NoError(t, err)
}
