// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// clock-config stages a Proposed RI static snapshot from a file or opt-in mTLS
// provisioning/v1 fetch. It never performs live apply.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"nexus-gateway/internal/provisioning"
)

func main() {
	snapshot := flag.String("snapshot", "", "RI clock-provisioning/1-draft JSON file")
	url := flag.String("provisioning-url", "", "Opt-in HTTPS provisioning/v1 base URL; mutually exclusive with --snapshot; stages only, never applies")
	ca := flag.String("provisioning-ca", "", "Provisioning server CA PEM (empty = system roots)")
	cert := flag.String("provisioning-cert", "", "Gateway mTLS client certificate PEM")
	key := flag.String("provisioning-key", "", "Gateway mTLS client private key PEM")
	serverName := flag.String("provisioning-servername", "", "Provisioning TLS server name override")
	timeout := flag.Duration("provisioning-timeout", 10*time.Second, "Timeout per HTTPS attempt (up to three attempts)")
	gateway := flag.String("gateway-id", "", "expected gateway ID")
	connector := flag.String("connector-id", "", "expected BACnet connector ID")
	device := flag.Int("device-id", -1, "expected BACnet device instance")
	mapping := flag.Bool("normalizer", false, "emit separate Normalizer mapping instead of native connector bundle")
	flag.Parse()
	data, err := readSnapshot(*snapshot, *url, provisioning.ClockScope{GatewayID: *gateway, ConnectorID: *connector, DeviceID: *device}, provisioning.ClockHTTPOptions{
		TLS: provisioning.TLSOptions{CAFile: *ca, CertFile: *cert, KeyFile: *key, ServerName: *serverName}, Timeout: *timeout,
	})
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

func readSnapshot(path, baseURL string, scope provisioning.ClockScope, opts provisioning.ClockHTTPOptions) ([]byte, error) {
	if (path == "") == (baseURL == "") {
		return nil, fmt.Errorf("exactly one of --snapshot or --provisioning-url is required")
	}
	if path != "" {
		return os.ReadFile(path)
	}
	client, err := provisioning.NewClockHTTPClient(baseURL, scope, opts)
	if err != nil {
		return nil, err
	}
	snapshot, err := client.Fetch(context.Background(), "")
	if err != nil {
		return nil, err
	}
	return snapshot.Bytes(), nil
}
