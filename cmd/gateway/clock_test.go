// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClockSpecRequiresSeparateNATSAuthentication(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "producer-auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"user":"producer","password":"synthetic"}`), 0600))
	for _, env := range [][]string{
		{"BACNET_ADDRESS=192.0.2.1"},
		{"BACNET_ADDRESS=192.0.2.1", "NATS_URL=nats://broker:4222"},
		{"BACNET_ADDRESS=192.0.2.1", "NATS_AUTH_FILE=" + auth},
		{"BACNET_ADDRESS=192.0.2.1", "NATS_URL=nats://user:secret@broker:4222", "NATS_AUTH_FILE=" + auth},
		{"BACNET_ADDRESS=192.0.2.1", "NATS_URL=nats://broker:4222", "NATS_AUTH_FILE=relative.json"},
		{"BACNET_ADDRESS=192.0.2.1", "NATS_URL=nats://broker:4222", "NATS_AUTH_FILE=" + auth, "NATS_URL=nats://other:4222"},
	} {
		body, err := json.Marshal(map[string]any{"ID": "bacnet-test", "Image": "example.com/bacnet@sha256:" + strings.Repeat("a", 64), "Env": env})
		require.NoError(t, err)
		path := filepath.Join(dir, "spec.json")
		require.NoError(t, os.WriteFile(path, body, 0600))
		_, err = loadClockSpec(path)
		require.Error(t, err, "managed mode must not inherit Core credentials")
		if err != nil {
			require.NotContains(t, err.Error(), "secret")
		}
	}
}

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
	auth := filepath.Join(t.TempDir(), "producer-auth.json")
	require.NoError(t, os.WriteFile(auth, []byte(`{"user":"producer","password":"synthetic"}`), 0600))
	body := `{"ID":"bacnet-boptest","Image":"example.com/bacnet@sha256:` + strings.Repeat("a", 64) + `","Env":["BACNET_ADDRESS=192.0.2.1","NATS_URL=nats://broker:4222","NATS_AUTH_FILE=` + auth + `"]}`
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
