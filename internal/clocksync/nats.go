// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package clocksync

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"nexus-gateway/internal/lifecycle"
)

// ConnectorNATSAuthFile validates the managed producer's explicit connection.
// Only a path is carried in specs/status/container environment; credentials are
// read by Python from a single readonly bind mount, never copied from Core.
func ConnectorNATSAuthFile(spec lifecycle.ConnectorSpec) (string, error) {
	values := map[string]string{}
	for _, entry := range spec.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "NATS_") {
			continue
		}
		if !ok || (key != "NATS_URL" && key != "NATS_AUTH_FILE") || values[key] != "" || value == "" {
			return "", fmt.Errorf("managed NATS requires unique NATS_URL and NATS_AUTH_FILE, without inline authentication")
		}
		values[key] = value
	}
	if values["NATS_URL"] == "" {
		return "", fmt.Errorf("managed connector requires its own NATS_URL")
	}
	for _, address := range strings.Split(values["NATS_URL"], ",") {
		u, err := url.Parse(strings.TrimSpace(address))
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "nats" && u.Scheme != "tls" && u.Scheme != "ws" && u.Scheme != "wss") {
			return "", fmt.Errorf("managed NATS_URL requires explicit server URLs without authentication, query or fragment")
		}
	}
	path := values["NATS_AUTH_FILE"]
	if !filepath.IsAbs(path) || strings.ContainsAny(path, ":\r\n") {
		return "", fmt.Errorf("managed connector requires an absolute NATS_AUTH_FILE path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("managed NATS_AUTH_FILE must be an existing regular file")
	}
	return path, nil
}
