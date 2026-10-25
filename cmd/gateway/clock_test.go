// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClockModeIsExplicitAndExclusive(t *testing.T) {
	require.NoError(t, validateClockMode(false, provisioningModeFallback, "url", "file", "", -1, true, true), "legacy behavior is not constrained")
	require.NoError(t, validateClockMode(true, provisioningModeURL, "https://example.invalid", "", t.TempDir(), 599, false, false))
	for _, mode := range []provisioningMode{provisioningModeFile, provisioningModeFallback} {
		require.Error(t, validateClockMode(true, mode, "https://example.invalid", "", t.TempDir(), 599, false, false))
	}
	require.Error(t, validateClockMode(true, provisioningModeURL, "https://example.invalid", "legacy.csv", t.TempDir(), 599, false, false))
	require.Error(t, validateClockMode(true, provisioningModeURL, "https://example.invalid", "", t.TempDir(), 599, true, false))
	require.Error(t, validateClockMode(true, provisioningModeURL, "https://example.invalid", "", t.TempDir(), 599, false, true))
	require.Error(t, validateClockMode(true, provisioningModeURL, "https://example.invalid", "", "relative", 599, false, false))
}

func TestClockSpecRequiresPinnedImageAndNoIndependentPoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector.json")
	body := `{"ID":"bacnet-boptest","Image":"example.com/bacnet@sha256:` + strings.Repeat("a", 64) + `","Env":["BACNET_ADDRESS=192.0.2.1"]}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0600))
	spec, err := loadClockSpec(path)
	require.NoError(t, err)
	require.Equal(t, "bacnet-boptest", spec.ID)
	for _, bad := range []string{strings.Replace(body, "@sha256:"+strings.Repeat("a", 64), ":latest", 1), strings.Replace(body, "BACNET_ADDRESS=192.0.2.1", "BACNET_POINTS=[]", 1), body + "{}"} {
		require.NoError(t, os.WriteFile(path, []byte(bad), 0600))
		_, err = loadClockSpec(path)
		require.Error(t, err)
	}
}
