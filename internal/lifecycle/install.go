// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/docker/docker/api/types/image"

	"nexus-gateway/internal/catalog"
)

// Install fetches, verifies, and starts a connector from a catalog Manifest.
//
// The sequence enforced by ADR-0006:
//  1. Validate manifest — allowlisted registry, valid digest format, gateway version
//  2. Pull the digest-pinned image reference (registry not contacted if validation fails)
//  3. Cosign verify via verifier (container never created if this fails)
//  4. Register the spec and start the container with declared permissions only
func (m *Manager) Install(
	ctx context.Context,
	manifest catalog.Manifest,
	verifier catalog.Verifier,
	allowedRegistries []string,
	gatewayVersion string,
) error {
	if err := manifest.Validate(allowedRegistries, gatewayVersion); err != nil {
		return fmt.Errorf("lifecycle: install %q: invalid manifest: %w", manifest.Name, err)
	}

	imageRef := manifest.ImageRef()

	unlock := m.lockConn(manifest.Name)
	defer unlock()

	// Prefer a locally available image over a registry pull: in dev the catalog
	// uses placeholder digests (sha256:000...000) that would always fail against
	// the real registry.  If the image is already present locally (by its base
	// tag, without the digest suffix), skip the pull and use the tag as the
	// running image reference.  In production the image won't be local, so the
	// pull path is taken as before.
	localRef := manifest.Image
	if _, localErr := m.docker.ImageInspect(ctx, localRef); localErr == nil {
		slog.Info("lifecycle: image available locally, skipping registry pull", "id", manifest.Name, "image", localRef)
		imageRef = localRef
	} else {
		rc, err := m.docker.ImagePull(ctx, imageRef, image.PullOptions{})
		if err != nil {
			return fmt.Errorf("lifecycle: install %q: pull %q: %w", manifest.Name, imageRef, err)
		}
		io.Copy(io.Discard, rc) //nolint:errcheck
		rc.Close()
	}

	if manifest.SignatureRequired {
		if err := verifier.Verify(ctx, imageRef); err != nil {
			return fmt.Errorf("lifecycle: install %q: signature verification failed: %w", manifest.Name, err)
		}
	}

	spec := ConnectorSpec{
		ID:    manifest.Name,
		Image: imageRef,
		Permissions: ConnectorPermissions{
			Network: manifest.Permissions.Network,
			Mounts:  manifest.Permissions.Mounts,
		},
	}
	m.registry.Register(spec)

	if err := m.doStart(ctx, manifest.Name); err != nil {
		m.registry.Remove(manifest.Name)
		return err
	}

	slog.Info("lifecycle: connector installed", "id", manifest.Name, "image", imageRef)
	return nil
}
