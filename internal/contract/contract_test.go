package contract

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	pb "nexus-gateway/gen"
)

func TestCurrentBuildingOSValueCasesCompile(t *testing.T) {
	frames := []*pb.TelemetryFrame{
		{Value: &pb.TelemetryFrame_ValueNum{ValueNum: 0}},
		{Value: &pb.TelemetryFrame_ValueStr{ValueStr: ""}},
		{Value: &pb.TelemetryFrame_ValueBool{ValueBool: false}},
	}
	assert.IsType(t, &pb.TelemetryFrame_ValueNum{}, frames[0].GetValue())
	assert.IsType(t, &pb.TelemetryFrame_ValueStr{}, frames[1].GetValue())
	assert.IsType(t, &pb.TelemetryFrame_ValueBool{}, frames[2].GetValue())
}

func TestLegacyNumericFieldThreeDecodesAsValueNum(t *testing.T) {
	wire := protowire.AppendTag(nil, 3, protowire.Fixed64Type)
	wire = protowire.AppendFixed64(wire, math.Float64bits(22.5))
	frame := &pb.TelemetryFrame{}
	require.NoError(t, proto.Unmarshal(wire, frame))
	assert.IsType(t, &pb.TelemetryFrame_ValueNum{}, frame.GetValue())
	assert.Equal(t, 22.5, frame.GetValueNum())
}

func TestExplicitZeroRetainsOneofPresence(t *testing.T) {
	want := &pb.TelemetryFrame{Value: &pb.TelemetryFrame_ValueNum{ValueNum: 0}}
	wire, err := proto.Marshal(want)
	require.NoError(t, err)
	got := &pb.TelemetryFrame{}
	require.NoError(t, proto.Unmarshal(wire, got))
	assert.IsType(t, &pb.TelemetryFrame_ValueNum{}, got.GetValue())
}

func TestVendoredContractMatchesBuildingOS(t *testing.T) {
	for _, name := range []string{"gateway_ingress.proto", "gateway_egress.proto"} {
		local, err := os.ReadFile(filepath.Join("..", "..", "proto", name))
		require.NoError(t, err)
		upstreamPath := filepath.Join("..", "..", "..", "gutp-building-os-ri", "proto", name)
		upstream, err := os.ReadFile(upstreamPath)
		if os.IsNotExist(err) {
			t.Skipf("Building OS sibling repository is unavailable: %s", upstreamPath)
		}
		require.NoError(t, err)
		assert.Equal(t, canonicalProto(upstream), canonicalProto(local), name)
	}
}

func canonicalProto(data []byte) string {
	withoutComments := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(data), "")
	withoutGoPackage := regexp.MustCompile(`(?m)\s*option\s+go_package\s*=\s*"[^"]+"\s*;`).ReplaceAllString(withoutComments, "")
	return strings.Join(strings.Fields(withoutGoPackage), "")
}

func TestCurrentBuildingOSEgressStatusCompiles(t *testing.T) {
	up := &pb.EgressUp{M: &pb.EgressUp_Status{Status: &pb.GatewayStatus{AppliedRevision: "etag"}}}
	assert.Equal(t, "etag", up.GetStatus().GetAppliedRevision())
}
