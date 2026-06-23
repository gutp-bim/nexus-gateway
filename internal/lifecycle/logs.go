// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bufio"
	"context"
	"fmt"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
	"strings"
)

// Logs returns the most recent `tail` log lines from the connector's container.
// Returns ErrConnectorNotFound if the connector is not registered.
// If the connector is not running (no ContainerID), returns an empty slice.
func (m *Manager) Logs(ctx context.Context, id string, tail int) ([]string, error) {
	status, ok := m.registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("lifecycle: logs %q: %w", id, ErrConnectorNotFound)
	}
	if status.ContainerID == "" {
		return []string{}, nil
	}
	rc, err := m.docker.ContainerLogs(ctx, status.ContainerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return nil, fmt.Errorf("lifecycle: logs %q: %w", id, err)
	}
	defer rc.Close()

	var sb strings.Builder
	if _, err := stdcopy.StdCopy(&sb, &sb, rc); err != nil {
		return nil, fmt.Errorf("lifecycle: logs %q: demux: %w", id, err)
	}

	var lines []string
	sc := bufio.NewScanner(strings.NewReader(sb.String()))
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}
