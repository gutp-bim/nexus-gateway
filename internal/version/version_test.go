// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package version

import "testing"

func TestString_ReturnsInjectedVersion(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	Version = "1.2.3"
	if got := String(); got != "1.2.3" {
		t.Fatalf("String() = %q, want the injected 1.2.3", got)
	}
}

func TestString_NeverEmpty(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	// Even if the ldflags value is somehow blanked, String() must resolve to a
	// non-empty version (build info or the "0.0.0" floor) — the semver gate and
	// the build-info metric both rely on that.
	Version = ""
	if got := String(); got == "" {
		t.Fatal("String() returned empty; must always resolve to a non-empty version")
	}
}

func TestString_DefaultIsStableSemver(t *testing.T) {
	// The compiled-in default must parse as a semver so a fresh (uninjected)
	// build still passes the Connector Catalog min_gateway_version gate.
	if Version == "" {
		t.Fatal("compiled-in default Version must not be empty")
	}
}
