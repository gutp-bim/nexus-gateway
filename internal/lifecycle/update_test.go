// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/catalog"
	"nexus-gateway/internal/lifecycle"
)

const (
	digestV1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestV2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func installedSpec(digest string) lifecycle.ConnectorSpec {
	return lifecycle.ConnectorSpec{
		ID:    "opcua-connector",
		Image: "ghcr.io/myorg/opcua-connector@" + digest,
		Permissions: lifecycle.ConnectorPermissions{
			Mounts: []string{"/var/lib/opcua"},
		},
	}
}

func v2Manifest() catalog.Manifest {
	return catalog.Manifest{
		Name:              "opcua-connector",
		Version:           "1.3.0",
		Image:             "ghcr.io/myorg/opcua-connector",
		Digest:            digestV2,
		MinGatewayVersion: "1.0.0",
		Permissions: catalog.Permissions{
			Mounts: []string{"/var/lib/opcua"},
		},
		SignatureRequired: false,
	}
}

// ── Manager.Update ────────────────────────────────────────────────────────────

// TestUpdate_NoopOnSameDigest verifies that when the catalog manifest's digest
// matches the installed version, no pull or stop is issued.
func TestUpdate_NoopOnSameDigest(t *testing.T) {
	ctx := context.Background()
	mock := newMockDocker("ctr-v1")
	reg := lifecycle.NewRegistry()
	reg.Register(installedSpec(digestV1))
	reg.SetRunning("opcua-connector", "ctr-v1", true)
	mgr := lifecycle.NewManager(mock, reg)

	// manifest has SAME digest as installed version
	sameManifest := v2Manifest()
	sameManifest.Digest = digestV1

	err := mgr.Update(ctx, "opcua-connector", sameManifest, catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0", 50*time.Millisecond)
	require.NoError(t, err)

	assert.Equal(t, 0, mock.calls("pull"), "no pull when digest unchanged")
	assert.Equal(t, 0, mock.calls("stop"), "no stop when digest unchanged")
}

// TestUpdate_AppliesUpdateSuccessfully verifies the happy path:
// new digest → pull → verify → stop old → start new → soak passes.
func TestUpdate_AppliesUpdateSuccessfully(t *testing.T) {
	ctx := context.Background()
	mock := newMockDocker("ctr-v1")
	mock.setInspectRunning(true)
	reg := lifecycle.NewRegistry()
	reg.Register(installedSpec(digestV1))
	reg.SetRunning("opcua-connector", "ctr-v1", true)
	mgr := lifecycle.NewManager(mock, reg)

	mock.setNextID("ctr-v2")

	err := mgr.Update(ctx, "opcua-connector", v2Manifest(), catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0", 50*time.Millisecond)
	require.NoError(t, err)

	assert.Equal(t, 1, mock.calls("pull"), "must pull new image")
	assert.Equal(t, 1, mock.calls("stop"), "must stop old container")
	assert.Equal(t, 1, mock.calls("remove"), "must remove old container")
	assert.GreaterOrEqual(t, mock.calls("create"), 1, "must create new container")

	status, ok := reg.Get("opcua-connector")
	require.True(t, ok)
	assert.True(t, status.Running)
	assert.True(t, strings.HasSuffix(status.Spec.Image, digestV2),
		"image must be updated to v2 digest; got %s", status.Spec.Image)
	assert.Equal(t, "ctr-v2", status.ContainerID)

	// Previous image must be retained for potential rollback
	assert.True(t, strings.HasSuffix(status.Spec.PrevImage, digestV1),
		"PrevImage must retain v1 digest; got %s", status.Spec.PrevImage)
}

// TestUpdate_RollbackOnUnhealthyContainer verifies that when the new container
// exits within the soak window, the manager automatically rolls back to the
// previous digest without a pull (registry may be unreachable).
func TestUpdate_RollbackOnUnhealthyContainer(t *testing.T) {
	ctx := context.Background()
	mock := newMockDocker("ctr-v1")
	mock.setInspectRunning(true)
	reg := lifecycle.NewRegistry()
	reg.Register(installedSpec(digestV1))
	reg.SetRunning("opcua-connector", "ctr-v1", true)
	mgr := lifecycle.NewManager(mock, reg)

	// New container will immediately fail (not running on inspect).
	// setLockRunning prevents ContainerStart from overriding inspectRunning=false.
	mock.setNextID("ctr-v2")
	mock.setInspectRunning(false)
	mock.setLockRunning(true) // keep inspectRunning=false even after ContainerStart

	err := mgr.Update(ctx, "opcua-connector", v2Manifest(), catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0", 100*time.Millisecond)
	require.Error(t, err, "Update must return error on rollback")
	assert.Contains(t, err.Error(), "rollback")

	// After rollback: connector must be running with previous image
	status, ok := reg.Get("opcua-connector")
	require.True(t, ok)
	assert.True(t, status.Running, "connector must be running after rollback")
	assert.True(t, strings.HasSuffix(status.Spec.Image, digestV1),
		"after rollback, image must be v1 digest; got %s", status.Spec.Image)
}

// TestUpdate_RollbackNoPull verifies that rollback does NOT attempt a pull
// (previous image is already local; registry may be unreachable).
func TestUpdate_RollbackNoPull(t *testing.T) {
	ctx := context.Background()
	mock := newMockDocker("ctr-v1")
	mock.setInspectRunning(true)
	reg := lifecycle.NewRegistry()
	reg.Register(installedSpec(digestV1))
	reg.SetRunning("opcua-connector", "ctr-v1", true)
	mgr := lifecycle.NewManager(mock, reg)

	mock.setNextID("ctr-v2")
	mock.setInspectRunning(false)
	mock.setLockRunning(true) // trigger rollback

	_ = mgr.Update(ctx, "opcua-connector", v2Manifest(), catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0", 100*time.Millisecond)

	// pull count must be exactly 1 (new image pull only; rollback uses local image)
	assert.Equal(t, 1, mock.calls("pull"),
		"rollback must NOT pull the previous image (already local)")
}

// TestUpdate_RollbackRemovesFailedContainer verifies that rollback removes the
// failed new container before starting the previous one, so that the fixed
// container name "nexus-<id>" is not already in use by Docker.
func TestUpdate_RollbackRemovesFailedContainer(t *testing.T) {
	ctx := context.Background()
	mock := newMockDocker("ctr-v1")
	mock.setInspectRunning(true)
	reg := lifecycle.NewRegistry()
	reg.Register(installedSpec(digestV1))
	reg.SetRunning("opcua-connector", "ctr-v1", true)
	mgr := lifecycle.NewManager(mock, reg)

	mock.setNextID("ctr-v2")
	mock.setInspectRunning(false)
	mock.setLockRunning(true) // trigger rollback via failed soak

	_ = mgr.Update(ctx, "opcua-connector", v2Manifest(), catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0", 100*time.Millisecond)

	// remove count: 1 for old (v1) container during update + 1 for failed (v2) container during rollback
	assert.GreaterOrEqual(t, mock.calls("remove"), 2,
		"rollback must remove the failed container before restarting previous image")
}

// TestUpdater_SkipsWrongManifestName verifies that the Updater drops a catalog
// response whose Name field does not match the requested connector ID.
func TestUpdater_SkipsWrongManifestName(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	mock := newMockDocker("ctr-v1")
	mock.setInspectRunning(true)
	reg := lifecycle.NewRegistry()
	reg.Register(installedSpec(digestV1))
	reg.SetRunning("opcua-connector", "ctr-v1", true)
	mgr := lifecycle.NewManager(mock, reg)

	// Catalog returns a manifest whose Name is a different connector.
	wrong := v2Manifest()
	wrong.Name = "different-connector"
	cat := &staticCatalogClient{manifest: wrong}
	updater := lifecycle.NewUpdater(mgr, reg, cat, catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0",
		lifecycle.UpdaterConfig{SoakWindow: 10 * time.Millisecond})
	go updater.Run(ctx, 20*time.Millisecond)

	<-ctx.Done()
	assert.Equal(t, 0, mock.calls("pull"),
		"updater must not apply an update when catalog returns wrong manifest name")
}

// TestUpdate_UnknownConnector verifies that Update returns an error for an
// unregistered connector ID.
func TestUpdate_UnknownConnector(t *testing.T) {
	ctx := context.Background()
	mock := newMockDocker("x")
	reg := lifecycle.NewRegistry()
	mgr := lifecycle.NewManager(mock, reg)

	err := mgr.Update(ctx, "does-not-exist", v2Manifest(), catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0", 50*time.Millisecond)
	require.Error(t, err)
	assert.Equal(t, 0, mock.calls("pull"))
}

// ── Updater polling loop ──────────────────────────────────────────────────────

// TestUpdater_PollsAndUpdates verifies that the Updater detects a new catalog
// version and applies the update within one poll interval.
func TestUpdater_PollsAndUpdates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mock := newMockDocker("ctr-v1")
	mock.setInspectRunning(true)
	reg := lifecycle.NewRegistry()
	reg.Register(installedSpec(digestV1))
	reg.SetRunning("opcua-connector", "ctr-v1", true)
	mgr := lifecycle.NewManager(mock, reg)

	mock.setNextID("ctr-v2")

	cat := &staticCatalogClient{manifest: v2Manifest()} // catalog has v2
	updater := lifecycle.NewUpdater(mgr, reg, cat, catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0",
		lifecycle.UpdaterConfig{SoakWindow: 50 * time.Millisecond})

	go updater.Run(ctx, 50*time.Millisecond)

	assert.Eventually(t, func() bool {
		status, ok := reg.Get("opcua-connector")
		return ok && status.Running && strings.HasSuffix(status.Spec.Image, digestV2)
	}, 3*time.Second, 50*time.Millisecond, "updater must apply catalog v2 within poll interval")
}

// TestUpdater_SkipsNonRunningConnectors verifies that the Updater does not
// attempt updates for connectors that are not currently running.
func TestUpdater_SkipsNonRunningConnectors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	mock := newMockDocker("ctr-v1")
	reg := lifecycle.NewRegistry()
	reg.Register(installedSpec(digestV1))
	// connector is installed but NOT running (e.g., stopped by operator)
	mgr := lifecycle.NewManager(mock, reg)

	cat := &staticCatalogClient{manifest: v2Manifest()}
	updater := lifecycle.NewUpdater(mgr, reg, cat, catalog.NoopVerifier{},
		[]string{"ghcr.io/myorg"}, "1.0.0",
		lifecycle.UpdaterConfig{SoakWindow: 10 * time.Millisecond})
	go updater.Run(ctx, 20*time.Millisecond)

	<-ctx.Done()
	assert.Equal(t, 0, mock.calls("pull"),
		"updater must not pull for non-running connectors")
}

// ── test helpers ─────────────────────────────────────────────────────────────

// staticCatalogClient always returns the same manifest (simulates a catalog
// that already has a newer version available).
type staticCatalogClient struct {
	manifest catalog.Manifest
}

func (c *staticCatalogClient) Fetch(_ context.Context, _ string) (catalog.Manifest, error) {
	return c.manifest, nil
}

func (c *staticCatalogClient) List(_ context.Context) ([]catalog.Manifest, error) {
	return []catalog.Manifest{c.manifest}, nil
}
