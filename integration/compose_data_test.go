// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// composeConfig is the slice of `docker compose config --format json` this test reads.
type composeConfig struct {
	Services map[string]struct {
		Environment map[string]string `json:"environment"`
		Volumes     []struct {
			Type     string `json:"type"`
			Source   string `json:"source"`
			Target   string `json:"target"`
			ReadOnly bool   `json:"read_only"`
		} `json:"volumes"`
	} `json:"services"`
	Volumes map[string]json.RawMessage `json:"volumes"`
}

// The Gateway writes the Store-and-Forward buffer and the synced Point List under
// /data. Every documented Compose deployment must back that directory with durable
// storage, or recreating the container (e.g. to change the Building OS address)
// destroys the queued telemetry (#175). The check runs on the MERGED config of each
// documented overlay combination, so an overlay that replaced the volume list
// would be caught too.
func TestCompose_GatewayDataDirIsDurable(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available")
	}
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose plugin not available")
	}

	combos := map[string][]string{
		"base":              {},
		"live-bos":          {"docker-compose.live-bos.yml"},
		"live-bos+sos-demo": {"docker-compose.live-bos.yml", "docker-compose.sos-demo.yml"},
		"integration":       {"docker-compose.integration.yml"},
		"opcua-simgw":       {"docker-compose.opcua-simgw.yml"},
		"soak":              {"docker-compose.soak.yml"},
		"external-keycloak": {"docker-compose.external-keycloak.yml"},
	}
	for name, overlays := range combos {
		t.Run(name, func(t *testing.T) {
			args := []string{"compose", "-f", "docker-compose.yml"}
			for _, o := range overlays {
				args = append(args, "-f", o)
			}
			args = append(args, "config", "--format", "json")
			cmd := exec.Command("docker", args...)
			cmd.Dir = ".." // repo root
			cmd.Env = append(os.Environ(), "KC_BASE=http://keycloak.invalid", "KC_REALM=test")
			out, err := cmd.Output() // stdout only: compose prints warnings on stderr
			require.NoError(t, err, "docker compose config")

			var cfg composeConfig
			require.NoError(t, json.Unmarshal(out, &cfg))
			gw, ok := cfg.Services["gateway"]
			require.True(t, ok, "gateway service")

			var durable bool
			for _, v := range gw.Volumes {
				if v.Target != "/data" || v.ReadOnly {
					continue
				}
				switch v.Type {
				case "volume":
					_, declared := cfg.Volumes[v.Source]
					durable = v.Source != "" && declared
				case "bind":
					durable = v.Source != ""
				}
			}
			assert.True(t, durable, "gateway /data must be a declared named volume or a writable bind mount, got %+v", gw.Volumes)

			for _, key := range []string{"SF_DB", "POINT_LIST_PERSIST"} {
				assert.True(t, strings.HasPrefix(gw.Environment[key], "/data/"),
					"%s must point into the durable /data directory, got %q", key, gw.Environment[key])
			}
		})
	}
}
