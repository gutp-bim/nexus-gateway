// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"

	"nexus-gateway/internal/catalog"
)

func containerRemoveOpts() container.RemoveOptions { return container.RemoveOptions{} }

// Update atomically replaces a running connector with a new version from a catalog Manifest.
//
// ADR-0006 sequence:
//  1. Validate manifest; compare digest — return nil immediately if unchanged (no pull).
//  2. Pull new digest-pinned image.
//  3. Cosign verify (if SignatureRequired).
//  4. Stop old container.
//  5. Start new container with manifest permissions.
//  6. Health soak: poll ContainerInspect for soakWindow; if container exits → rollback.
//  7. Rollback: start old image without a pull (already local); return wrapped error.
func (m *Manager) Update(
	ctx context.Context,
	id string,
	manifest catalog.Manifest,
	verifier catalog.Verifier,
	allowedRegistries []string,
	gatewayVersion string,
	soakWindow time.Duration,
) error {
	if err := manifest.Validate(allowedRegistries, gatewayVersion); err != nil {
		return fmt.Errorf("lifecycle: update %q: invalid manifest: %w", id, err)
	}

	unlock := m.lockConn(id)
	defer unlock()

	current, ok := m.registry.Get(id)
	if !ok {
		return fmt.Errorf("lifecycle: update %q: %w", id, ErrConnectorNotFound)
	}

	newImageRef := manifest.ImageRef()

	// Skip if the installed digest already matches the catalog manifest.
	if digestFromRef(current.Spec.Image) == manifest.Digest {
		return nil
	}

	// Pull the new digest-pinned image.
	rc, err := m.docker.ImagePull(ctx, newImageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("lifecycle: update %q: pull %q: %w", id, newImageRef, err)
	}
	io.Copy(io.Discard, rc) //nolint:errcheck
	rc.Close()

	// Cosign verify before touching the running container.
	if manifest.SignatureRequired {
		if err := verifier.Verify(ctx, newImageRef); err != nil {
			return fmt.Errorf("lifecycle: update %q: signature verification failed: %w", id, err)
		}
	}

	oldContainerID := current.ContainerID
	oldImage := current.Spec.Image

	// Stop and remove the old container.
	if oldContainerID != "" {
		if err := m.doStop(ctx, id); err != nil {
			return fmt.Errorf("lifecycle: update %q: stop old container: %w", id, err)
		}
		if err := m.docker.ContainerRemove(ctx, oldContainerID, containerRemoveOpts()); err != nil {
			slog.Warn("lifecycle: update: remove old container failed (continuing)", "id", id, "err", err)
		}
	}

	// Register the new spec (preserving old image as PrevImage for rollback).
	newSpec := ConnectorSpec{
		ID:        id,
		Image:     newImageRef,
		PrevImage: oldImage,
		Env:       current.Spec.Env,
		Permissions: ConnectorPermissions{
			Network: manifest.Permissions.Network,
			Mounts:  manifest.Permissions.Mounts,
		},
	}
	m.registry.Register(newSpec)

	// Start the new container.
	if err := m.doStart(ctx, id); err != nil {
		return m.rollback(ctx, id, oldImage, fmt.Errorf("start new container: %w", err))
	}

	// Health soak: verify the new container stays running for soakWindow.
	newStatus, _ := m.registry.Get(id)
	if err := m.soakCheck(ctx, newStatus.ContainerID, soakWindow); err != nil {
		return m.rollback(ctx, id, oldImage, err)
	}

	slog.Info("lifecycle: connector updated", "id", id, "image", newImageRef)
	return nil
}

// rollback restores the previous image without a pull (it is already local).
// It always returns a non-nil error wrapping both the original failure and any rollback error.
// Uses context.Background() so Docker calls succeed even when the parent ctx is cancelled
// (e.g., during gateway shutdown while a soak window was in progress).
func (m *Manager) rollback(_ context.Context, id, prevImage string, cause error) error {
	rCtx := context.Background()
	slog.Warn("lifecycle: update failed — rolling back", "id", id, "prev_image", prevImage, "cause", cause)

	prev, ok := m.registry.Get(id)
	if !ok || prevImage == "" {
		return fmt.Errorf("lifecycle: update %q: rollback: no previous image available: %w", id, cause)
	}
	// Capture the failed container ID before doStop clears it from the registry.
	failedContainerID := prev.ContainerID

	// Stop the new (failed) container if it is still running.
	_ = m.doStop(rCtx, id)
	// Remove the failed container so doStart can reuse the fixed name "nexus-<id>".
	if failedContainerID != "" {
		if err := m.docker.ContainerRemove(rCtx, failedContainerID, containerRemoveOpts()); err != nil {
			slog.Warn("lifecycle: rollback: remove failed container failed (continuing)", "id", id, "err", err)
		}
	}

	// Restore the previous spec and start without a pull.
	rollbackSpec := ConnectorSpec{
		ID:          id,
		Image:       prevImage,
		PrevImage:   prev.Spec.PrevImage, // preserve any earlier rollback chain
		Env:         prev.Spec.Env,
		Permissions: prev.Spec.Permissions,
	}
	m.registry.Register(rollbackSpec)

	if startErr := m.doStart(rCtx, id); startErr != nil {
		return fmt.Errorf("lifecycle: update %q: rollback failed (connector is stopped): %w; original: %v", id, startErr, cause)
	}

	slog.Info("lifecycle: rollback complete", "id", id, "image", prevImage)
	return fmt.Errorf("lifecycle: update %q: rollback triggered: %w", id, cause)
}

// soakCheck polls ContainerInspect until soakWindow elapses or the container exits.
// Inspects before sleeping so every tick — including the one that crosses the deadline
// boundary — always gets a health check.
func (m *Manager) soakCheck(ctx context.Context, containerID string, soakWindow time.Duration) error {
	if containerID == "" || soakWindow <= 0 {
		return nil
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(soakWindow)
	for {
		resp, err := m.docker.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("health soak: inspect failed: %w", err)
		}
		if resp.ContainerJSONBase == nil || resp.State == nil || !resp.State.Running {
			return fmt.Errorf("health soak: container exited within soak window")
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Rollback restores the connector to its previous image without a pull
// (the previous image is already local — ADR-0006). Returns an error if the
// connector has no previous image recorded in the registry.
func (m *Manager) Rollback(ctx context.Context, id string) error {
	unlock := m.lockConn(id)
	defer unlock()

	current, ok := m.registry.Get(id)
	if !ok {
		return fmt.Errorf("lifecycle: rollback %q: %w", id, ErrConnectorNotFound)
	}
	if current.Spec.PrevImage == "" {
		return fmt.Errorf("lifecycle: rollback %q: no previous image available", id)
	}

	prevImage := current.Spec.PrevImage
	currentContainerID := current.ContainerID

	// Use a background context for all Docker operations: if the HTTP request
	// context is cancelled (client disconnect, server timeout), the rollback must
	// still complete rather than leaving the connector in a stopped state.
	dCtx := context.Background()

	if err := m.doStop(dCtx, id); err != nil {
		return fmt.Errorf("lifecycle: rollback %q: stop: %w", id, err)
	}
	if currentContainerID != "" {
		if err := m.docker.ContainerRemove(dCtx, currentContainerID, containerRemoveOpts()); err != nil {
			slog.Warn("lifecycle: rollback: remove current container failed (continuing)", "id", id, "err", err)
		}
	}

	// Swap current↔prev so the old "current" becomes recoverable via a second rollback.
	m.registry.Register(ConnectorSpec{
		ID:          id,
		Image:       prevImage,
		PrevImage:   current.Spec.Image,
		Env:         current.Spec.Env,
		Permissions: current.Spec.Permissions,
	})

	if err := m.doStart(dCtx, id); err != nil {
		return fmt.Errorf("lifecycle: rollback %q: start: %w", id, err)
	}

	slog.Info("lifecycle: connector rolled back", "id", id, "image", prevImage)
	return nil
}

// digestFromRef extracts "sha256:…" from "registry/img@sha256:…".
func digestFromRef(ref string) string {
	if idx := strings.Index(ref, "@"); idx >= 0 {
		return ref[idx+1:]
	}
	return ""
}

// ── Updater ───────────────────────────────────────────────────────────────────

// UpdaterConfig holds tuning parameters for the Updater.
type UpdaterConfig struct {
	SoakWindow time.Duration // soak window for new container health check (default: 30s)
}

// Updater polls the Connector Catalog at a configurable interval and applies
// available updates to installed, running connectors (ADR-0006 poll-only model).
type Updater struct {
	mgr       *Manager
	registry  *Registry
	catalog   catalog.Client
	verifier  catalog.Verifier
	allowlist []string
	gwVersion string
	cfg       UpdaterConfig
}

func NewUpdater(mgr *Manager, reg *Registry, cat catalog.Client, verifier catalog.Verifier, allowlist []string, gwVersion string, cfg UpdaterConfig) *Updater {
	if cfg.SoakWindow <= 0 {
		cfg.SoakWindow = 30 * time.Second
	}
	return &Updater{
		mgr:       mgr,
		registry:  reg,
		catalog:   cat,
		verifier:  verifier,
		allowlist: allowlist,
		gwVersion: gwVersion,
		cfg:       cfg,
	}
}

// Run polls the catalog at the given interval and applies updates until ctx is cancelled.
func (u *Updater) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.poll(ctx)
		}
	}
}

func (u *Updater) poll(ctx context.Context) {
	for _, status := range u.registry.List() {
		if !status.Running {
			continue // do not update stopped connectors
		}
		manifest, err := u.catalog.Fetch(ctx, status.Spec.ID)
		if err != nil {
			slog.Warn("updater: catalog fetch failed", "id", status.Spec.ID, "err", err)
			continue
		}
		// Guard against misbehaving catalog servers returning a manifest for a
		// different connector than requested (HTTPClient path does not validate this).
		if manifest.Name != status.Spec.ID {
			slog.Warn("updater: catalog returned unexpected manifest name", "requested", status.Spec.ID, "got", manifest.Name)
			continue
		}
		if err := u.mgr.Update(ctx, status.Spec.ID, manifest, u.verifier, u.allowlist, u.gwVersion, u.cfg.SoakWindow); err != nil {
			slog.Error("updater: update failed", "id", status.Spec.ID, "err", err)
		}
	}
}
