// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	dockernetwork "github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/catalog"
	"nexus-gateway/internal/lifecycle"
)

const (
	testAllowedRegistry = "ghcr.io/myorg"
	testGatewayVersion  = "1.0.0"
	testDigest          = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func testManifest() catalog.Manifest {
	return catalog.Manifest{
		Name:              "opcua-connector",
		Version:           "1.2.3",
		Image:             "ghcr.io/myorg/opcua-connector",
		Digest:            testDigest,
		MinGatewayVersion: "1.0.0",
		Permissions: catalog.Permissions{
			Network: []string{"opc.tcp"},
			Mounts:  []string{"/var/lib/opcua"},
		},
		SignatureRequired: true,
	}
}

// TestInstall_Succeeds verifies the happy path: valid manifest, noop verifier →
// pull digest-pinned image → container created → connector starts.
func TestInstall_Succeeds(t *testing.T) {
	ctx := context.Background()
	mock := newCapturingMockDocker("ctr-01")
	reg := lifecycle.NewRegistry()
	mgr := lifecycle.NewManager(mock, reg)

	m := testManifest()
	err := mgr.Install(ctx, m, catalog.NoopVerifier{}, []string{testAllowedRegistry}, testGatewayVersion)
	require.NoError(t, err)

	assert.Equal(t, 1, mock.calls("pull"))
	assert.Equal(t, 1, mock.calls("create"))
	assert.Equal(t, 1, mock.calls("start"))

	// Image reference must be digest-pinned
	assert.Equal(t, m.ImageRef(), mock.lastCreateImage())

	entries := reg.List()
	require.Len(t, entries, 1)
	assert.True(t, entries[0].Running)
	assert.Equal(t, "opcua-connector", entries[0].Spec.ID)
	assert.Equal(t, m.ImageRef(), entries[0].Spec.Image)
}

// TestInstall_UnsignedRefused verifies that when the verifier returns an error,
// the container is never created.
func TestInstall_UnsignedRefused(t *testing.T) {
	ctx := context.Background()
	mock := newCapturingMockDocker("ctr-01")
	reg := lifecycle.NewRegistry()
	mgr := lifecycle.NewManager(mock, reg)

	failVerifier := errVerifier{err: errors.New("no valid signature found")}
	err := mgr.Install(ctx, testManifest(), failVerifier, []string{testAllowedRegistry}, testGatewayVersion)
	require.Error(t, err)

	// Pull may have been attempted, but container must never be created
	assert.Equal(t, 0, mock.calls("create"), "container must not be created when signature verification fails")
	assert.Empty(t, reg.List(), "registry must be empty after refused install")
}

// TestInstall_InvalidManifest verifies that a manifest that fails validation
// (no digest) is rejected before any Docker call is made.
func TestInstall_InvalidManifest(t *testing.T) {
	ctx := context.Background()
	mock := newCapturingMockDocker("ctr-01")
	reg := lifecycle.NewRegistry()
	mgr := lifecycle.NewManager(mock, reg)

	m := testManifest()
	m.Digest = "" // tag-only — must be rejected
	err := mgr.Install(ctx, m, catalog.NoopVerifier{}, []string{testAllowedRegistry}, testGatewayVersion)
	require.ErrorIs(t, err, catalog.ErrNoDigest)

	assert.Equal(t, 0, mock.calls("pull"), "pull must not be attempted for invalid manifest")
	assert.Equal(t, 0, mock.calls("create"))
}

// TestInstall_RegistryNotAllowed verifies that a non-allowlisted registry is
// rejected before any pull is attempted.
func TestInstall_RegistryNotAllowed(t *testing.T) {
	ctx := context.Background()
	mock := newCapturingMockDocker("ctr-01")
	reg := lifecycle.NewRegistry()
	mgr := lifecycle.NewManager(mock, reg)

	m := testManifest()
	m.Image = "docker.io/evil/connector"
	err := mgr.Install(ctx, m, catalog.NoopVerifier{}, []string{testAllowedRegistry}, testGatewayVersion)
	require.ErrorIs(t, err, catalog.ErrRegistryNotAllowed)

	assert.Equal(t, 0, mock.calls("pull"), "pull must not be attempted for non-allowlisted registry")
}

// TestInstall_PermissionContract verifies that the container is created with
// exactly the mounts declared in the manifest permissions — no more, no less.
func TestInstall_PermissionContract(t *testing.T) {
	ctx := context.Background()
	mock := newCapturingMockDocker("ctr-01")
	reg := lifecycle.NewRegistry()
	mgr := lifecycle.NewManager(mock, reg)

	m := testManifest()
	m.Permissions.Mounts = []string{"/var/lib/opcua", "/etc/opcua/config"}

	err := mgr.Install(ctx, m, catalog.NoopVerifier{}, []string{testAllowedRegistry}, testGatewayVersion)
	require.NoError(t, err)

	hostCfg := mock.lastHostConfig()
	require.NotNil(t, hostCfg)

	mountPaths := make([]string, len(hostCfg.Binds))
	for i, b := range hostCfg.Binds {
		mountPaths[i] = b
	}
	assert.Contains(t, mountPaths, "/var/lib/opcua:/var/lib/opcua:ro")
	assert.Contains(t, mountPaths, "/etc/opcua/config:/etc/opcua/config:ro")
	assert.Len(t, hostCfg.Binds, 2, "container must have exactly the declared mounts")
}

// ── test helpers ─────────────────────────────────────────────────────────────

// capturingMockDocker extends mockDocker to record ContainerCreate arguments.
type capturingMockDocker struct {
	*mockDocker
	lastImage  string
	lastHost   *container.HostConfig
}

func newCapturingMockDocker(id string) *capturingMockDocker {
	return &capturingMockDocker{mockDocker: newMockDocker(id)}
}

func (c *capturingMockDocker) ContainerCreate(ctx context.Context, cfg *container.Config, hc *container.HostConfig, nc *dockernetwork.NetworkingConfig, pl *ocispec.Platform, name string) (container.CreateResponse, error) {
	c.mockDocker.mu.Lock()
	c.lastImage = cfg.Image
	c.lastHost = hc
	c.mockDocker.mu.Unlock()
	return c.mockDocker.ContainerCreate(ctx, cfg, hc, nc, pl, name)
}

func (c *capturingMockDocker) lastCreateImage() string {
	c.mockDocker.mu.Lock()
	defer c.mockDocker.mu.Unlock()
	return c.lastImage
}

func (c *capturingMockDocker) lastHostConfig() *container.HostConfig {
	c.mockDocker.mu.Lock()
	defer c.mockDocker.mu.Unlock()
	return c.lastHost
}

// errVerifier always returns the configured error.
type errVerifier struct{ err error }

func (v errVerifier) Verify(_ context.Context, _ string) error { return v.err }
