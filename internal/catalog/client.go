// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// Client fetches connector manifests from a catalog source.
type Client interface {
	Fetch(ctx context.Context, name string) (Manifest, error)
	// List returns all connector manifests available in the catalog.
	List(ctx context.Context) ([]Manifest, error)
}

// HTTPClient fetches manifests from a remote catalog server.
type HTTPClient struct {
	baseURL string
	http    *http.Client
}

func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{baseURL: baseURL, http: http.DefaultClient}
}

func (c *HTTPClient) List(ctx context.Context) ([]Manifest, error) {
	url := fmt.Sprintf("%s/connectors", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog: HTTP list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog: list: server returned %d", resp.StatusCode)
	}
	var manifests []Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifests); err != nil {
		return nil, fmt.Errorf("catalog: decode manifests: %w", err)
	}
	return manifests, nil
}

func (c *HTTPClient) Fetch(ctx context.Context, name string) (Manifest, error) {
	url := fmt.Sprintf("%s/connectors/%s", c.baseURL, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Manifest{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("catalog: HTTP fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Manifest{}, fmt.Errorf("%w: %q", ErrManifestNotFound, name)
	}
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("catalog: server returned %d", resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("catalog: decode manifest: %w", err)
	}
	return m, nil
}

// FileClient loads manifests from a local JSON file containing a []Manifest array.
// Intended for dev / docker-compose environments where a real catalog server is absent.
type FileClient struct {
	path string
}

func NewFileClient(path string) *FileClient {
	return &FileClient{path: path}
}

func (c *FileClient) List(_ context.Context) ([]Manifest, error) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil, fmt.Errorf("catalog: read file %q: %w", c.path, err)
	}
	var manifests []Manifest
	if err := json.Unmarshal(data, &manifests); err != nil {
		return nil, fmt.Errorf("catalog: parse catalog file: %w", err)
	}
	return manifests, nil
}

func (c *FileClient) Fetch(_ context.Context, name string) (Manifest, error) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return Manifest{}, fmt.Errorf("catalog: read file %q: %w", c.path, err)
	}
	var manifests []Manifest
	if err := json.Unmarshal(data, &manifests); err != nil {
		return Manifest{}, fmt.Errorf("catalog: parse catalog file: %w", err)
	}
	for _, m := range manifests {
		if m.Name == name {
			return m, nil
		}
	}
	return Manifest{}, fmt.Errorf("%w: %q", ErrManifestNotFound, name)
}
