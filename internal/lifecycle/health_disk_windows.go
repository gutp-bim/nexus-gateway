// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package lifecycle

func diskStatsMB() (usedMB, totalMB float64) { return 0, 0 }
