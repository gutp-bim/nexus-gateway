// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ContainerClient is the Docker Engine subset the Manager needs.
type ContainerClient interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *dockernetwork.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error)
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
}

// Manager orchestrates connector container lifecycle via the Docker Engine API.
// All public methods (Start/Stop/Restart/Upgrade) and the Watch restart path
// acquire a per-connector lock so that concurrent operations on the same
// connector are serialised — prevents duplicate containers and orphaned IDs.
type Manager struct {
	docker   ContainerClient
	registry *Registry
	mu       sync.Mutex              // guards connLock map
	connLock map[string]*sync.Mutex  // per-connector serialisation lock
}

func NewManager(docker ContainerClient, registry *Registry) *Manager {
	return &Manager{
		docker:   docker,
		registry: registry,
		connLock: make(map[string]*sync.Mutex),
	}
}

// lockConn acquires the per-connector mutex and returns an unlock function.
func (m *Manager) lockConn(id string) func() {
	m.mu.Lock()
	if _, ok := m.connLock[id]; !ok {
		m.connLock[id] = &sync.Mutex{}
	}
	mu := m.connLock[id]
	m.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// tryLockConn attempts to acquire the per-connector mutex without blocking.
// Returns (unlock, true) on success, (nil, false) if the mutex is already held.
func (m *Manager) tryLockConn(id string) (func(), bool) {
	m.mu.Lock()
	if _, ok := m.connLock[id]; !ok {
		m.connLock[id] = &sync.Mutex{}
	}
	mu := m.connLock[id]
	m.mu.Unlock()
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}

// Start creates and starts a container for the given connector ID.
func (m *Manager) Start(ctx context.Context, id string) error {
	unlock := m.lockConn(id)
	defer unlock()
	return m.doStart(ctx, id)
}

// doStart is the unlocked implementation called by Start and Watch's restart path.
func (m *Manager) doStart(ctx context.Context, id string) error {
	status, ok := m.registry.Get(id)
	if !ok {
		return fmt.Errorf("lifecycle: connector %q: %w", id, ErrConnectorNotFound)
	}
	containerID, err := m.create(ctx, status.Spec)
	if err != nil {
		return fmt.Errorf("lifecycle: create container for %q: %w", id, err)
	}
	if err := m.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("lifecycle: start container for %q: %w", id, err)
	}
	m.registry.SetRunning(id, containerID, true)
	slog.Info("lifecycle: connector started", "id", id, "container", containerID)
	return nil
}

// Stop stops the container for the given connector ID.
func (m *Manager) Stop(ctx context.Context, id string) error {
	unlock := m.lockConn(id)
	defer unlock()
	return m.doStop(ctx, id)
}

// doStop is the unlocked implementation called by Stop and Upgrade.
func (m *Manager) doStop(ctx context.Context, id string) error {
	status, ok := m.registry.Get(id)
	if !ok {
		return fmt.Errorf("lifecycle: connector %q: %w", id, ErrConnectorNotFound)
	}
	if status.ContainerID == "" {
		return nil // already stopped
	}
	timeout := 10
	if err := m.docker.ContainerStop(ctx, status.ContainerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("lifecycle: stop container for %q: %w", id, err)
	}
	m.registry.SetRunning(id, "", false)
	slog.Info("lifecycle: connector stopped", "id", id)
	return nil
}

// Restart stops the existing container and starts a new one.
func (m *Manager) Restart(ctx context.Context, id string) error {
	unlock := m.lockConn(id)
	defer unlock()

	status, ok := m.registry.Get(id)
	if !ok {
		return fmt.Errorf("lifecycle: connector %q: %w", id, ErrConnectorNotFound)
	}
	if status.ContainerID != "" {
		timeout := 10
		if err := m.docker.ContainerStop(ctx, status.ContainerID, container.StopOptions{Timeout: &timeout}); err != nil {
			return fmt.Errorf("lifecycle: restart stop phase for %q: %w", id, err)
		}
		if err := m.docker.ContainerRemove(ctx, status.ContainerID, container.RemoveOptions{}); err != nil {
			return fmt.Errorf("lifecycle: restart remove phase for %q: %w", id, err)
		}
		m.registry.SetRunning(id, "", false)
	}
	return m.doStart(ctx, id)
}

// Upgrade pulls a new image, stops the current container, and starts a new one with the new image.
func (m *Manager) Upgrade(ctx context.Context, id, newImage string) error {
	unlock := m.lockConn(id)
	defer unlock()

	// Capture full spec once — avoids TOCTOU from multiple registry.Get calls.
	status, ok := m.registry.Get(id)
	if !ok {
		return fmt.Errorf("lifecycle: connector %q: %w", id, ErrConnectorNotFound)
	}
	oldContainerID := status.ContainerID
	env := status.Spec.Env

	// Pull new image before stopping to minimise downtime.
	rc, err := m.docker.ImagePull(ctx, newImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("lifecycle: pull image %q: %w", newImage, err)
	}
	io.Copy(io.Discard, rc) //nolint:errcheck
	rc.Close()

	if err := m.doStop(ctx, id); err != nil {
		return err
	}
	if oldContainerID != "" {
		_ = m.docker.ContainerRemove(ctx, oldContainerID, container.RemoveOptions{})
	}

	// Update spec with new image; preserve PrevImage for rollback chain.
	m.registry.Register(ConnectorSpec{ID: id, Image: newImage, PrevImage: status.Spec.Image, Env: env})
	return m.doStart(ctx, id)
}

// Watch polls the Docker daemon for connector container liveness at the given interval,
// auto-restarting any that have stopped. Runs until ctx is cancelled.
func (m *Manager) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, status := range m.registry.List() {
				if !status.Running || status.ContainerID == "" {
					continue
				}
				resp, err := m.docker.ContainerInspect(ctx, status.ContainerID)
				if err != nil {
					// Transient Docker API error — do not treat as stopped to avoid
					// orphaning a live container. Log and retry on the next tick.
					slog.Warn("lifecycle: ContainerInspect failed — skipping restart", "id", status.Spec.ID, "err", err)
					continue
				}
				if resp.ContainerJSONBase == nil || resp.State == nil || resp.State.Running {
					continue // healthy
				}
				slog.Warn("lifecycle: connector container stopped — restarting", "id", status.Spec.ID)
				unlock, ok := m.tryLockConn(status.Spec.ID)
				if !ok {
					// Connector is being updated; skip this tick.
					continue
				}
				m.registry.SetRunning(status.Spec.ID, "", false)
				if restartErr := m.doStart(ctx, status.Spec.ID); restartErr != nil {
					slog.Error("lifecycle: restart failed", "id", status.Spec.ID, "err", restartErr)
				}
				unlock()
			}
		}
	}
}

func (m *Manager) create(ctx context.Context, spec ConnectorSpec) (string, error) {
	hc := &container.HostConfig{}
	for _, mount := range spec.Permissions.Mounts {
		hc.Binds = append(hc.Binds, mount+":"+mount+":ro")
	}

	resp, err := m.docker.ContainerCreate(ctx,
		&container.Config{
			Image: spec.Image,
			Env:   spec.Env,
		},
		hc,
		nil, nil,
		"nexus-"+spec.ID,
	)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}
