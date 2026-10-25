// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"nexus-gateway/internal/lifecycle"
)

func loadClockSpec(path string) (lifecycle.ConnectorSpec, error) {
	var spec lifecycle.ConnectorSpec
	data, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&spec); err != nil {
		return spec, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return spec, fmt.Errorf("clock connector spec: trailing JSON")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(spec.ID) || !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(spec.Image) {
		return spec, fmt.Errorf("clock connector requires a single-token ID and preloaded digest-pinned image")
	}
	address := false
	for _, env := range spec.Env {
		key, value, ok := strings.Cut(env, "=")
		if !ok {
			return spec, fmt.Errorf("invalid connector environment entry")
		}
		if key == "BACNET_ADDRESS" && value != "" {
			address = true
		}
		if key == "BACNET_POINTS" || key == "BACNET_CLOCK_CONFIG_FILE" || key == "BACNET_MANAGED_SESSION" {
			return spec, fmt.Errorf("static point/session configuration cannot be combined with clock provisioning")
		}
	}
	if !address {
		return spec, fmt.Errorf("clock connector requires explicit isolated BACNET_ADDRESS")
	}
	return spec, nil
}

func validateClockMode(enabled bool, mode provisioningMode, url, file, state string, device int, devSim, catalog bool) error {
	if !enabled {
		return nil
	}
	if url == "" || file != "" || (mode != provisioningModeAuto && mode != provisioningModeURL) || devSim || catalog || !filepath.IsAbs(state) || device < 0 || device > 4194302 {
		return fmt.Errorf("clock provisioning requires URL-only, absolute state directory, explicit device, no file/fallback/dev-sim/catalog")
	}
	return nil
}
