// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package version is the single source of truth for the gateway build version.
// Everything that needs the version — the --version flag, the /health payload,
// the gateway_build_info metric, and the Connector Catalog min_gateway_version
// install/update gate — reads it from here, so the value is defined exactly once.
package version

import "runtime/debug"

// Version is the gateway build version. Release builds override it via
//
//	-ldflags "-X nexus-gateway/internal/version.Version=1.2.3"
//
// The compiled-in default keeps local, `go run`, and CI builds working (and
// satisfying the min_gateway_version gate) without an injected value.
var Version = "0.1.0"

// String returns the resolved gateway version: the ldflags-injected (or
// default) Version when set, otherwise the module version Go embeds in the
// binary, otherwise "0.0.0". It never returns an empty string.
func String() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "0.0.0"
}

// Revision is the VCS commit the binary was built from, or "" when Go embedded
// none (a non-VCS tree, or -buildvcs=false).
//
// Version alone cannot identify a running image: it is a hand-maintained semver
// that stays "0.1.0" across many commits, so a container built from a stale
// layer reports exactly the same value as a current one. That is how a 24h soak
// ran for 12+ hours against an image predating the /health/live route while the
// compose healthcheck already required it (#120) — nothing the running process
// exposed could reveal the drift. The revision makes it observable.
func Revision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}
