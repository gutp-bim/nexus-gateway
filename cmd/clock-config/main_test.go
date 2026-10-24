// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"nexus-gateway/internal/provisioning"
)

func TestReadSnapshotSourceSelection(t *testing.T) {
	scope := provisioning.ClockScope{GatewayID: "GW-BOPTEST-001", ConnectorID: "bacnet-boptest", DeviceID: 599}
	path := "../../test/contracts/builder-ri-9083af2/provisioning.json"
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	got, err := readSnapshot(path, "", scope, provisioning.ClockHTTPOptions{})
	require.NoError(t, err)
	require.Equal(t, want, got)
	_, err = readSnapshot(path, "https://example.invalid", scope, provisioning.ClockHTTPOptions{})
	require.ErrorContains(t, err, "exactly one")
	_, err = readSnapshot("", "", scope, provisioning.ClockHTTPOptions{})
	require.ErrorContains(t, err, "exactly one")
	_, err = readSnapshot("", "https://example.invalid", scope, provisioning.ClockHTTPOptions{})
	require.ErrorContains(t, err, "mTLS") // rejected before any dial
}
