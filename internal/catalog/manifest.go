// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrNoDigest           = errors.New("catalog: manifest must specify a digest (sha256:…)")
	ErrInvalidDigest      = errors.New("catalog: digest has invalid format")
	ErrRegistryNotAllowed = errors.New("catalog: image registry is not in the allowlist")
	ErrVersionTooOld      = errors.New("catalog: gateway version is older than manifest min_gateway_version")
	ErrManifestNotFound   = errors.New("catalog: manifest not found")
)

// digestPattern matches sha256:<64 hex chars> — the only format we accept.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Permissions declares the container capabilities a connector is allowed to use.
type Permissions struct {
	Network []string `json:"network"` // e.g. ["opc.tcp"]
	Mounts  []string `json:"mounts"`  // host paths to bind-mount read-only
}

// Manifest is an entry in the Connector Catalog (ADR-0006).
type Manifest struct {
	Name              string      `json:"name"`
	Version           string      `json:"version"`
	Image             string      `json:"image"`            // registry/image (no tag, no digest)
	Digest            string      `json:"digest"`           // sha256:<64 hex chars>
	MinGatewayVersion string      `json:"min_gateway_version"`
	Permissions       Permissions `json:"permissions"`
	SignatureRequired bool        `json:"signature_required"`
}

// ImageRef returns the digest-pinned reference used for pull and container creation.
func (m Manifest) ImageRef() string {
	return m.Image + "@" + m.Digest
}

// Validate checks that the manifest is safe to install:
//   - digest is present and valid
//   - image registry is in allowedRegistries
//   - gatewayVersion satisfies min_gateway_version
func (m Manifest) Validate(allowedRegistries []string, gatewayVersion string) error {
	if m.Digest == "" {
		return ErrNoDigest
	}
	if !digestPattern.MatchString(m.Digest) {
		return fmt.Errorf("%w: %q", ErrInvalidDigest, m.Digest)
	}
	if !registryAllowed(m.Image, allowedRegistries) {
		return fmt.Errorf("%w: image %q", ErrRegistryNotAllowed, m.Image)
	}
	if m.MinGatewayVersion != "" {
		if semverLess(gatewayVersion, m.MinGatewayVersion) {
			return fmt.Errorf("%w: gateway %s < required %s", ErrVersionTooOld, gatewayVersion, m.MinGatewayVersion)
		}
	}
	return nil
}

// registryAllowed returns true when the image's host prefix matches one of the allowed entries.
func registryAllowed(image string, allowed []string) bool {
	for _, a := range allowed {
		if strings.HasPrefix(image, a) {
			return true
		}
	}
	return false
}

// semverLess returns true when a < b using simple major.minor.patch comparison.
// Non-numeric segments are compared lexicographically (graceful degradation).
func semverLess(a, b string) bool {
	ap := parseSemver(a)
	bp := parseSemver(b)
	for i := 0; i < 3; i++ {
		if ap[i] != bp[i] {
			return ap[i] < bp[i]
		}
	}
	return false
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	var major, minor, patch int
	fmt.Sscanf(v, "%d.%d.%d", &major, &minor, &patch) //nolint:errcheck
	return [3]int{major, minor, patch}
}
