// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package lifecycle

import "syscall"

func diskStatsMB() (usedMB, totalMB float64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0, 0
	}
	total := float64(st.Blocks) * float64(st.Bsize)
	free := float64(st.Bavail) * float64(st.Bsize)
	return (total - free) / 1024 / 1024, total / 1024 / 1024
}
