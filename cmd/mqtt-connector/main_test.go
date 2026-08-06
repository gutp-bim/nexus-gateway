package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPointEnv_FileTakesPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "points.json")
	require.NoError(t, os.WriteFile(path, []byte(`[{"topic":"from/file"}]`), 0o600))
	points, err := loadPointEnv(`[{"topic":"from/env"}]`, path)
	require.NoError(t, err)
	require.Len(t, points, 1)
	assert.Equal(t, "from/file", points[0].Topic)
}

func TestLoadPointEnv_HandlesTwoThousandPoints(t *testing.T) {
	want := make([]pointEnv, 2000)
	for i := range want {
		want[i] = pointEnv{Topic: fmt.Sprintf("takenaka.co.jp/Tokyo/THX/device-%04d/value/R", i)}
	}
	data, err := json.Marshal(want)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "points.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	got, err := loadPointEnv("[]", path)
	require.NoError(t, err)
	assert.Len(t, got, 2000)
	assert.Equal(t, want[1999].Topic, got[1999].Topic)
}

func TestParseSubscriptionEnv(t *testing.T) {
	subs, err := parseSubscriptionEnv(`[{"filter":"takenaka.co.jp/Tokyo/THX/#","qos":1}]`)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	assert.Equal(t, "takenaka.co.jp/Tokyo/THX/#", subs[0].Filter)
	assert.Equal(t, byte(1), subs[0].QoS)
}

func TestParseSubscriptionEnv_RejectsInvalid(t *testing.T) {
	for _, raw := range []string{`[{"filter":"","qos":1}]`, `[{"filter":"a/#","qos":2}]`} {
		_, err := parseSubscriptionEnv(raw)
		assert.Error(t, err)
	}
}

func TestParseStringListEnv(t *testing.T) {
	values, err := parseStringListEnv("MQTT_IGNORE_TOPICS", `["tas/heartbeat"]`)
	require.NoError(t, err)
	assert.Equal(t, []string{"tas/heartbeat"}, values)
}

func TestPayloadLimitDefault(t *testing.T) {
	assert.Equal(t, uint64(1024), defaultMaxPayloadBytes)
}

func TestEnvAliasPrefersEstablishedName(t *testing.T) {
	t.Setenv("MQTT_TLS_CA_FILE", "established.pem")
	t.Setenv("MQTT_CA_FILE", "alias.pem")
	assert.Equal(t, "established.pem", envAlias("MQTT_TLS_CA_FILE", "MQTT_CA_FILE"))
}

func TestEnvAliasFallsBackToAlias(t *testing.T) {
	t.Setenv("MQTT_TLS_CA_FILE", "")
	t.Setenv("MQTT_CA_FILE", "alias.pem")
	assert.Equal(t, "alias.pem", envAlias("MQTT_TLS_CA_FILE", "MQTT_CA_FILE"))
}

func TestParseDurationDefault(t *testing.T) {
	t.Setenv("MQTT_FRESHNESS_INTERVAL", "")
	got, err := parseDurationDefault("MQTT_FRESHNESS_INTERVAL", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, got)

	t.Setenv("MQTT_FRESHNESS_INTERVAL", "15s")
	got, err = parseDurationDefault("MQTT_FRESHNESS_INTERVAL", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, got)
}

func TestIsTruthy(t *testing.T) {
	assert.True(t, isTruthy("YES"))
	assert.False(t, isTruthy("false"))
}
