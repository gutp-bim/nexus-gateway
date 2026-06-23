// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"fmt"
	"os/exec"
)

// Verifier checks that a digest-pinned image reference carries a valid cosign signature.
type Verifier interface {
	Verify(ctx context.Context, imageRef string) error
}

// CosignVerifier calls the cosign CLI to verify the image signature.
//
// Key-based:    set KeyPath to a cosign public key file.
// Keyless OIDC: leave KeyPath empty and set Identity + OIDCIssuer
//               (e.g., Identity = GitHub Actions workflow URL,
//                       OIDCIssuer = "https://token.actions.githubusercontent.com").
type CosignVerifier struct {
	KeyPath    string // path to cosign public key; empty = keyless
	Identity   string // expected certificate identity (keyless only)
	OIDCIssuer string // expected OIDC issuer (keyless only)
}

func (v CosignVerifier) Verify(ctx context.Context, imageRef string) error {
	args := []string{"verify"}
	if v.KeyPath != "" {
		args = append(args, "--key", v.KeyPath)
	} else {
		if v.Identity != "" {
			args = append(args, "--certificate-identity", v.Identity)
		}
		if v.OIDCIssuer != "" {
			args = append(args, "--certificate-oidc-issuer", v.OIDCIssuer)
		}
	}
	args = append(args, imageRef)
	out, err := exec.CommandContext(ctx, "cosign", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("catalog: cosign verify %q: %w\n%s", imageRef, err, out)
	}
	return nil
}

// NoopVerifier always succeeds — for dev/CI environments where cosign is not available.
type NoopVerifier struct{}

func (NoopVerifier) Verify(_ context.Context, _ string) error { return nil }
