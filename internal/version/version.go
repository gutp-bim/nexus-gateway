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
