// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package clocksync

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"nexus-gateway/internal/lifecycle"
	"nexus-gateway/internal/provisioning"
)

type managedConnector struct {
	nc         *nats.Conn
	owner      *lifecycle.Exclusive
	base       lifecycle.ConnectorSpec
	store      *sessions
	record     *sessionRecord
	started    bool
	discovered bool
	stopping   *atomic.Bool
}

type runtimeReceipt struct {
	Capability string `json:"capability"`
	Session    string `json:"session"`
	Revision   string `json:"revision"`
	Hash       string `json:"native_sha256"`
	Paused     *bool  `json:"paused"`
}

func (d *managedConnector) request(ctx context.Context, action string) (runtimeReceipt, error) {
	var receipt runtimeReceipt
	if d.record == nil {
		return receipt, fmt.Errorf("missing producer session")
	}
	data, _ := json.Marshal(map[string]string{"action": action, "session": d.record.Session, "revision": d.record.snapshot.Revision(), "native_sha256": d.record.nativeHash})
	for {
		attempt, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		msg, err := d.nc.RequestWithContext(attempt, "cfg.bacnet."+d.base.ID+".runtime", data)
		cancel()
		if err == nil {
			if err = json.Unmarshal(msg.Data, &receipt); err != nil {
				return receipt, err
			}
			if receipt.Capability != "clock-runtime/1" || receipt.Session != d.record.Session || receipt.Revision != d.record.snapshot.Revision() || receipt.Hash != d.record.nativeHash || receipt.Paused == nil {
				return receipt, fmt.Errorf("running producer receipt mismatch")
			}
			return receipt, nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return receipt, ctx.Err()
		case <-timer.C:
		}
	}
}
func (d *managedConnector) Stop(ctx context.Context) error {
	err := d.owner.Stop(ctx)
	if err == nil {
		d.started = false
		d.discovered = true
	}
	return err
}
func (d *managedConnector) Configure(ctx context.Context, cfg provisioning.NativeClockConfig) error {
	if d.record == nil || cfg.Revision != d.record.snapshot.Revision() {
		return fmt.Errorf("missing prepared snapshot")
	}
	authFile, err := ConnectorNATSAuthFile(d.base)
	if err != nil {
		return err
	}
	spec := d.base
	spec.Env = nil
	reserved := map[string]bool{"BACNET_POINTS": true, "BACNET_CLOCK_CONFIG_FILE": true, "BACNET_MANAGED_SESSION": true, "BACNET_COV_ENABLED": true, "BACNET_DEVICE_ID": true, "CONNECTOR_ID": true}
	for _, e := range d.base.Env {
		key, _, _ := strings.Cut(e, "=")
		if !reserved[key] {
			spec.Env = append(spec.Env, e)
		}
	}
	spec.Env = append(spec.Env, "BACNET_CLOCK_CONFIG_FILE="+filepath.Join(d.store.dir, "native", d.record.Session+".json"), "BACNET_MANAGED_SESSION="+d.record.Session, "BACNET_COV_ENABLED=false", fmt.Sprintf("BACNET_DEVICE_ID=%d", cfg.DeviceID))
	spec.Permissions.Mounts = append(append([]string(nil), spec.Permissions.Mounts...), filepath.Join(d.store.dir, "native"))
	spec.Permissions.Mounts = append(spec.Permissions.Mounts, authFile)
	return d.owner.Configure(spec)
}
func (d *managedConnector) Start(ctx context.Context) error {
	if d.stopping.Load() {
		return fmt.Errorf("gateway shutdown requested")
	}
	d.started = true
	return d.owner.Start(ctx)
}
func (d *managedConnector) Revision(ctx context.Context) (string, error) {
	r, err := d.request(ctx, "status")
	if err != nil {
		return "", err
	}
	if !*r.Paused {
		return "", fmt.Errorf("candidate started without pause")
	}
	if err = d.store.verify(d.record); err != nil {
		return "", err
	}
	return r.Revision, nil
}
