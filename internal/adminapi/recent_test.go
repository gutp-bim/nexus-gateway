package adminapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "nexus-gateway/gen"
)

func TestRecentStore_PreservesTypedValues(t *testing.T) {
	store := NewRecentStore()
	store.Record(&pb.TelemetryFrame{PointId: "n", Value: &pb.TelemetryFrame_ValueNum{ValueNum: 0}})
	store.Record(&pb.TelemetryFrame{PointId: "s", Value: &pb.TelemetryFrame_ValueStr{ValueStr: "running"}})
	store.Record(&pb.TelemetryFrame{PointId: "b", Value: &pb.TelemetryFrame_ValueBool{ValueBool: false}})

	values := store.Snapshot()
	require.Len(t, values, 3)
	byPoint := make(map[string]any, len(values))
	for _, value := range values {
		byPoint[value.PointID] = value.Value
	}
	assert.Equal(t, float64(0), byPoint["n"])
	assert.Equal(t, "running", byPoint["s"])
	assert.Equal(t, false, byPoint["b"])
}
