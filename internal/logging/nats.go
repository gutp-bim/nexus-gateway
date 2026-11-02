// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"errors"
	"net/url"
	"strings"

	"github.com/nats-io/nats.go"
)

// NATSAddress retains only scheme and authority, never userinfo, paths, query
// parameters or fragments. Invalid input is replaced, not returned in an error.
func NATSAddress(address string) string {
	parts := strings.Split(address, ",")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, "://") {
			part = "nats://" + part
		}
		u, err := url.Parse(part)
		if err != nil || u.Hostname() == "" || strings.ContainsAny(u.Host, "@\\\r\n\t ") {
			parts[i] = "[invalid NATS address]"
			continue
		}
		switch u.Scheme {
		case "nats", "tls", "ws", "wss":
			parts[i] = (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
		default:
			parts[i] = "[invalid NATS address]"
		}
	}
	return strings.Join(parts, ",")
}

// NATSError uses known categories: URL parse errors and broker-controlled
// descriptions can contain credentials, so arbitrary error text is never logged.
func NATSError(err error) string {
	if err == nil {
		return "none"
	}
	for _, known := range []error{nats.ErrAuthorization, nats.ErrAuthExpired, nats.ErrAuthRevoked, nats.ErrNoServers, nats.ErrTimeout, nats.ErrConnectionClosed, nats.ErrDisconnected, nats.ErrPermissionViolation} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "NATS transport or configuration error"
}
