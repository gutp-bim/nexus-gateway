package logging

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestNATSAddressKeepsOnlyServerLocations(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"nats://user:secret@localhost:4222", "nats://localhost:4222"},
		{"tls://token@localhost:4222", "tls://localhost:4222"},
		{"wss://user:secret@[::1]:443/path?token=secret#secret", "wss://[::1]:443"},
		{"user:secret@host:4222, nats://token@other:4223", "nats://host:4222,nats://other:4223"},
		{"nats://user:secret@host:%zz", "[invalid NATS address]"},
		{"nats://user:secret@", "[invalid NATS address]"},
		{"invalid://secret", "[invalid NATS address]"},
	} {
		require.Equal(t, tc.want, NATSAddress(tc.input))
	}
}

func TestNATSErrorNeverFormatsUntrustedDetails(t *testing.T) {
	require.Equal(t, "NATS transport or configuration error", NATSError(errors.New("nats://user:secret@invalid:%zz")))
	require.Equal(t, nats.ErrAuthorization.Error(), NATSError(fmt.Errorf("secret: %w", nats.ErrAuthorization)))
	require.Equal(t, nats.ErrPermissionViolation.Error(), NATSError(fmt.Errorf("secret: %w", nats.ErrPermissionViolation)))
}
