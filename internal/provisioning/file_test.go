// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/pointlist"
	"nexus-gateway/internal/provisioning"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestFileClient_ServesCSVSnapshot(t *testing.T) {
	const csv = `point_id,writable,unit,object_type_bacnet,instance_no_bacnet
SOS-PT-001,false,C,analogInput,1
SOS-PT-010,true,,binaryOutput,2001
`
	path := writeFile(t, t.TempDir(), "pl.csv", csv)
	c := provisioning.NewFileClient(path, "bacnet-01", nil)

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Full)
	require.Len(t, result.Entries, 2)
	assert.Equal(t, "analogInput,1", result.Entries[0].LocalID)
	assert.Equal(t, "SOS-PT-001", result.Entries[0].PointID)
	assert.True(t, result.Entries[1].Writable)
}

func TestFileClient_ServesJSONSnapshot(t *testing.T) {
	const j = `[{"connector_id":"bacnet-01","protocol":"bacnet","local_id":"analogInput,1","point_id":"SOS-PT-001"}]`
	path := writeFile(t, t.TempDir(), "pl.json", j)
	c := provisioning.NewFileClient(path, "bacnet-01", nil)

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Entries, 1)
	assert.Equal(t, "SOS-PT-001", result.Entries[0].PointID)
}

func TestFileClient_ETagUnchanged_ReturnsNil(t *testing.T) {
	path := writeFile(t, t.TempDir(), "pl.csv",
		"point_id,object_type_bacnet,instance_no_bacnet\nSOS-PT-001,analogInput,1\n")
	c := provisioning.NewFileClient(path, "bacnet-01", nil)
	ctx := context.Background()

	r1, err := c.Fetch(ctx, "")
	require.NoError(t, err)
	require.NotNil(t, r1)

	// Fetch again with the returned ETag — must get nil (304)
	r2, err := c.Fetch(ctx, r1.ETag)
	require.NoError(t, err)
	assert.Nil(t, r2, "unchanged file must return nil (304)")
}

func TestFileClient_ETagChangesOnEdit(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "pl.csv",
		"point_id,object_type_bacnet,instance_no_bacnet\nSOS-PT-001,analogInput,1\n")
	c := provisioning.NewFileClient(path, "bacnet-01", nil)
	ctx := context.Background()

	r1, _ := c.Fetch(ctx, "")
	require.NotNil(t, r1)

	writeFile(t, dir, "pl.csv",
		"point_id,object_type_bacnet,instance_no_bacnet\nSOS-PT-001,analogInput,2\n")
	r2, err := c.Fetch(ctx, r1.ETag)
	require.NoError(t, err)
	require.NotNil(t, r2, "edited file must return a new result")
	assert.NotEqual(t, r1.ETag, r2.ETag, "ETag must change when file content changes")
}

func TestFileClient_ConnectorMap_ResolvesEachProtocol(t *testing.T) {
	const csv = `point_id,local_id,protocol,object_type_bacnet,instance_no_bacnet
PT001,,bacnet,analogInput,1
PT101,ns=2;s=PT001,opcua,,
PT201,sensors/room1/temp,mqtt,,
`
	path := writeFile(t, t.TempDir(), "pl.csv", csv)
	c := provisioning.NewFileClient(path, "default-connector", map[string]string{
		"bacnet": "bacnet-01",
		"opcua":  "opcua-01",
		"mqtt":   "mqtt-01",
	})

	result, err := c.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Entries, 3)
	assert.Equal(t, "bacnet-01", result.Entries[0].ConnectorID)
	assert.Equal(t, "opcua-01", result.Entries[1].ConnectorID)
	assert.Equal(t, "mqtt-01", result.Entries[2].ConnectorID)
}

func TestFileClient_UnsupportedExtensionErrors(t *testing.T) {
	path := writeFile(t, t.TempDir(), "pl.txt", "point_id\nX\n")
	_, err := provisioning.NewFileClient(path, "bacnet-01", nil).Fetch(context.Background(), "")
	require.Error(t, err, "a non-.csv/.json file must be rejected with a clear error")
}

func TestFileClient_MissingFileErrors(t *testing.T) {
	c := provisioning.NewFileClient("/nonexistent/pl.csv", "bacnet-01", nil)
	_, err := c.Fetch(context.Background(), "")
	assert.Error(t, err)
}

// FileClient must satisfy the provisioning.Client interface.
var _ provisioning.Client = (*provisioning.FileClient)(nil)

func TestFileClient_ImplementsClient(t *testing.T) {
	var c provisioning.Client = provisioning.NewFileClient("x.csv", "bacnet-01", nil)
	_ = c
	_ = []pointlist.Entry(nil)
}
