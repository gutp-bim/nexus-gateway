// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package pointlist_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus-gateway/internal/pointlist"
)

// A trimmed slice of the building-os-e2e-test mvp-pointlist.csv shape.
const sampleCSV = `gateway_id,device_id,point_id,point_name,writable,unit,description,local_id,object_type_bacnet,instance_no_bacnet
GW-SOS-001,SOS-DEV-001,SOS-PT-001,Entrance Temperature,false,C,temp,L-001,analogInput,1
GW-SOS-001,SOS-DEV-001,SOS-PT-002,Entrance Humidity,false,%,humidity,L-002,analogInput,2
GW-SOS-001,SOS-DEV-002,SOS-PT-010,Damper Command,true,,cmd,L-010,binaryOutput,2001
`

func TestLoadCSV_ProjectsBACnetNativeAddress(t *testing.T) {
	entries, err := pointlist.LoadCSV(strings.NewReader(sampleCSV), "bacnet-01")
	require.NoError(t, err)
	require.Len(t, entries, 3)

	e := entries[0]
	assert.Equal(t, "bacnet-01", e.ConnectorID)
	assert.Equal(t, "bacnet", e.Protocol)
	// Native address is object_type_bacnet + instance_no_bacnet, NOT the SBCO local_id column.
	assert.Equal(t, "analogInput,1", e.LocalID)
	assert.Equal(t, "SOS-PT-001", e.PointID)
	assert.Equal(t, "C", e.Unit)
	assert.False(t, e.Writable)
	assert.Equal(t, "SOS-DEV-001", e.DeviceRef)
}

func TestLoadCSV_ParsesWritable(t *testing.T) {
	entries, err := pointlist.LoadCSV(strings.NewReader(sampleCSV), "bacnet-01")
	require.NoError(t, err)
	assert.Equal(t, "binaryOutput,2001", entries[2].LocalID)
	assert.True(t, entries[2].Writable, "writable=true must be parsed")
}

func TestLoadCSV_SkipsRowsMissingBACnetAddressAndLocalID(t *testing.T) {
	const csv = `point_id,writable,unit,object_type_bacnet,instance_no_bacnet
SOS-PT-001,false,C,analogInput,1
SOS-PT-OPCUA,false,C,,
`
	entries, err := pointlist.LoadCSV(strings.NewReader(csv), "bacnet-01")
	require.NoError(t, err)
	require.Len(t, entries, 1, "rows without BACnet columns and without local_id are skipped")
	assert.Equal(t, "analogInput,1", entries[0].LocalID)
}

func TestLoadCSV_UsesLocalIDForNonBACnetRows(t *testing.T) {
	const csv = `point_id,writable,unit,local_id,connector_id,protocol
UA-PT-001,false,℃,ns=2;s=PT001,opcua-01,opcua
UA-PT-002,true,,ns=2;s=PT002,opcua-01,opcua
`
	entries, err := pointlist.LoadCSV(strings.NewReader(csv), "fallback-connector")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, "ns=2;s=PT001", entries[0].LocalID)
	assert.Equal(t, "opcua", entries[0].Protocol)
	assert.Equal(t, "opcua-01", entries[0].ConnectorID)
	assert.Equal(t, "℃", entries[0].Unit)
	assert.False(t, entries[0].Writable)

	assert.Equal(t, "ns=2;s=PT002", entries[1].LocalID)
	assert.True(t, entries[1].Writable)
}

func TestLoadCSV_MixedProtocolsInOnefile(t *testing.T) {
	const csv = `point_id,local_id,connector_id,protocol,object_type_bacnet,instance_no_bacnet,unit
PT001,"analogInput,1001",bacnet-01,bacnet,analogInput,1001,℃
PT101,ns=2;s=PT001,opcua-01,opcua,,,℃
`
	entries, err := pointlist.LoadCSV(strings.NewReader(csv), "default-connector")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, "analogInput,1001", entries[0].LocalID)
	assert.Equal(t, "bacnet", entries[0].Protocol)
	assert.Equal(t, "bacnet-01", entries[0].ConnectorID)

	assert.Equal(t, "ns=2;s=PT001", entries[1].LocalID)
	assert.Equal(t, "opcua", entries[1].Protocol)
	assert.Equal(t, "opcua-01", entries[1].ConnectorID)
}

func TestLoadCSV_RequiresPointIDColumn(t *testing.T) {
	const csv = `object_type_bacnet,instance_no_bacnet
analogInput,1
`
	_, err := pointlist.LoadCSV(strings.NewReader(csv), "bacnet-01")
	require.Error(t, err, "missing point_id column must error")
}

func TestLoadCSV_StripsBOMAndSkipsEmptyPointID(t *testing.T) {
	// Leading UTF-8 BOM (Excel export) + a row with a blank point_id.
	const csv = "\ufeffpoint_id,object_type_bacnet,instance_no_bacnet\n" +
		"SOS-PT-001,analogInput,1\n" +
		",analogInput,2\n"
	entries, err := pointlist.LoadCSV(strings.NewReader(csv), "bacnet-01")
	require.NoError(t, err, "BOM on the header must not break column matching")
	require.Len(t, entries, 1, "row with empty point_id must be skipped")
	assert.Equal(t, "SOS-PT-001", entries[0].PointID)
}

func TestLoadCSV_DedupesDuplicatePointID(t *testing.T) {
	const csv = `point_id,object_type_bacnet,instance_no_bacnet
SOS-PT-001,analogInput,1
SOS-PT-001,analogInput,99
`
	entries, err := pointlist.LoadCSV(strings.NewReader(csv), "bacnet-01")
	require.NoError(t, err)
	require.Len(t, entries, 1, "duplicate point_id must collapse to a single entry")
	assert.Equal(t, "analogInput,1", entries[0].LocalID, "first row wins")
}

func TestLoadCSV_ToleratesColumnReordering(t *testing.T) {
	const csv = `instance_no_bacnet,object_type_bacnet,point_id,writable
7,analogValue,SOS-PT-099,true
`
	entries, err := pointlist.LoadCSV(strings.NewReader(csv), "bacnet-01")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "analogValue,7", entries[0].LocalID)
	assert.Equal(t, "SOS-PT-099", entries[0].PointID)
	assert.True(t, entries[0].Writable)
}
