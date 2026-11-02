// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

var ErrManaged = errors.New("lifecycle: connector owned by clock provisioning")

const clockOwnerLabel = "nexus.clock.connector"

func (m *Manager) checkReserved(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reserved[id] {
		return ErrManaged
	}
	return nil
}

// Claim reserves the connector for its process lifetime, including halted
// recovery. Public admin/update/watch operations cannot bypass the reservation.
// No daemon request, image pull or process start is performed by Claim.
func (m *Manager) Claim(spec ConnectorSpec) (*Exclusive, error) {
	unlock := m.lockConn(spec.ID)
	defer unlock()
	if m.docker == nil || spec.ID == "" {
		return nil, fmt.Errorf("lifecycle: managed connector needs daemon client and ID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reserved == nil {
		m.reserved = map[string]bool{}
	}
	if m.reserved[spec.ID] {
		return nil, ErrManaged
	}
	m.reserved[spec.ID] = true
	m.registry.Register(spec)
	return &Exclusive{m: m, id: spec.ID}, nil
}

type Exclusive struct {
	m           *Manager
	id          string
	containerID string
}

// Stop also finds a labeled instance left by a previous gateway crash. An
// unowned container with the same name is never stopped or removed.
func (e *Exclusive) Stop(ctx context.Context) error {
	unlock := e.m.lockConn(e.id)
	defer unlock()
	id := e.containerID
	if id == "" {
		id = "nexus-" + e.id
	}
	info, err := e.m.docker.ContainerInspect(ctx, id)
	if dockerclient.IsErrNotFound(err) {
		e.containerID = ""
		e.m.registry.SetRunning(e.id, "", false)
		return nil
	}
	if err != nil {
		return err
	}
	if info.ContainerJSONBase == nil || info.Config == nil || info.Config.Labels[clockOwnerLabel] != e.id {
		return fmt.Errorf("lifecycle: refusing unowned container")
	}
	id = info.ID
	if id == "" {
		return fmt.Errorf("lifecycle: missing container identity")
	}
	e.containerID = id
	timeout := 10
	if info.State != nil && info.State.Running {
		if err := e.m.docker.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
			return err
		}
	}
	info, err = e.m.docker.ContainerInspect(ctx, id)
	if err != nil {
		return err
	}
	if info.State == nil || info.State.Running {
		return fmt.Errorf("lifecycle: producer termination unconfirmed")
	}
	if err := e.m.docker.ContainerRemove(ctx, id, container.RemoveOptions{}); err != nil {
		return err
	}
	e.containerID = ""
	e.m.registry.SetRunning(e.id, "", false)
	return nil
}

func (e *Exclusive) Configure(spec ConnectorSpec) error {
	unlock := e.m.lockConn(e.id)
	defer unlock()
	if e.containerID != "" || spec.ID != e.id {
		return fmt.Errorf("lifecycle: configure requires stopped owned connector")
	}
	e.m.registry.Register(spec)
	return nil
}

func (e *Exclusive) Start(ctx context.Context) error {
	unlock := e.m.lockConn(e.id)
	defer unlock()
	if e.containerID != "" {
		return fmt.Errorf("lifecycle: prior container still exists")
	}
	s, ok := e.m.registry.Get(e.id)
	if !ok {
		return ErrConnectorNotFound
	}
	id, err := e.m.createWithLabels(ctx, s.Spec, map[string]string{clockOwnerLabel: e.id})
	if err != nil {
		return err
	}
	// Retain identity even if ContainerStart partially succeeds and returns an
	// error; rollback must stop/remove that exact instance.
	e.containerID = id
	e.m.registry.SetRunning(e.id, id, false)
	if err := e.m.docker.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return err
	}
	e.m.registry.SetRunning(e.id, id, true)
	return nil
}
