// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// clock-config stages a Proposed RI static snapshot without network or live apply.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"nexus-gateway/internal/provisioning"
)

func main() {
	snapshot := flag.String("snapshot", "", "RI clock-provisioning/1-draft JSON file")
	gateway := flag.String("gateway-id", "", "expected gateway ID")
	connector := flag.String("connector-id", "", "expected BACnet connector ID")
	device := flag.Int("device-id", -1, "expected BACnet device instance")
	mapping := flag.Bool("normalizer", false, "emit separate Normalizer mapping instead of native connector bundle")
	flag.Parse()
	data, err := os.ReadFile(*snapshot)
	if err == nil {
		var cfg *provisioning.NativeClockConfig
		cfg, entries, stageErr := provisioning.StageClockSnapshot(data, *gateway, *connector, *device)
		err = stageErr
		if err == nil {
			var output any = cfg
			if *mapping {
				output = entries
			}
			err = json.NewEncoder(os.Stdout).Encode(output)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
