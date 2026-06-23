// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"nexus-gateway/internal/pointlist"
)

// FileClient is a file-backed provisioning.Client: it serves a Point List
// snapshot derived from a local CSV (SBCO point-list format) or JSON file.
//
// It is interface-compatible with the real Building OS provisioning API
// (HTTPClient, gutp-building-os-oss #224), so the gateway can sync from a file
// during dev/E2E and switch to the authoritative API without code changes. Per
// ADR-0003 a provisioning snapshot always overrides any local bootstrap.
//
// FileClient always returns a full result; diffs are not supported for files.
// The ETag is the SHA-256 content hash, so Fetch returns nil when the file is unchanged.
type FileClient struct {
	path        string
	connectorID string // used to stamp BACnet CSV entries
}

// NewFileClient serves the Point List from path. A .csv file is parsed via
// LoadCSV (BACnet native-address projection); a .json file is parsed as a JSON
// array of pointlist.Entry. Any other extension is rejected.
func NewFileClient(path, connectorID string) *FileClient {
	return &FileClient{path: path, connectorID: connectorID}
}

// Fetch implements Client. Returns nil when knownETag matches the file's content hash (304).
// Always returns a full result (no delta support for file sources).
func (c *FileClient) Fetch(_ context.Context, knownETag string) (*FetchResult, error) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil, fmt.Errorf("provisioning: read %q: %w", c.path, err)
	}

	etag := fmt.Sprintf("%x", sha256.Sum256(data))
	if etag == knownETag {
		return nil, nil // 304 — unchanged
	}

	entries, err := c.parse(data)
	if err != nil {
		return nil, err
	}
	return &FetchResult{ETag: etag, Full: true, Entries: entries}, nil
}

func (c *FileClient) parse(data []byte) ([]pointlist.Entry, error) {
	lower := strings.ToLower(c.path)
	switch {
	case strings.HasSuffix(lower, ".csv"):
		return pointlist.LoadCSV(strings.NewReader(string(data)), c.connectorID)
	case strings.HasSuffix(lower, ".json"):
		var entries []pointlist.Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("provisioning: parse %q as JSON: %w", c.path, err)
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("provisioning: unsupported file format %q (want .csv or .json)", c.path)
	}
}
