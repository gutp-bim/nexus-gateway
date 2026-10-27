// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
package clocksync

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	pb "nexus-gateway/gen"
	"nexus-gateway/internal/provisioning"
	rc "nexus-gateway/internal/runcontext"
)

// RunContextConfig is operator configuration, never a key supplied by telemetry.
// The ledger MUST already exist; recovery never silently initializes history.
type RunContextConfig struct {
	ClockID      string `json:"clockId"`
	PublicKey    []byte `json:"publicKey"`
	EnvelopeFile string `json:"envelopeFile"`
	LedgerFile   string `json:"ledgerFile"`
}

var errInvalidPoll = errors.New("invalid poll acquisition evidence")

type runBinding struct {
	config RunContextConfig
	ledger *rc.Ledger
}

func (g *Gateway) EnableRunContextFile(path string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held || g.producer.record != nil || g.run != nil {
		return errors.New("run context must be selected before provisioning")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg RunContextConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.ClockID == "" || len(cfg.PublicKey) != ed25519.PublicKeySize || !filepath.IsAbs(cfg.EnvelopeFile) || !filepath.IsAbs(cfg.LedgerFile) {
		return errors.New("invalid run context configuration")
	}
	ledger, err := rc.Open(cfg.LedgerFile, rc.Scope{GatewayID: g.store.scope.GatewayID, ClockID: cfg.ClockID}, cfg.PublicKey)
	if err != nil {
		return err
	}
	raw, err = os.ReadFile(cfg.EnvelopeFile)
	if err != nil {
		return err
	}
	var envelope rc.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if err = ledger.Commit(envelope, time.Now().UTC()); err != nil {
		return err
	}
	for _, sub := range []string{"run-context", "run-frames"} {
		if err = os.MkdirAll(filepath.Join(g.store.dir, sub), 0700); err != nil {
			return err
		}
	}
	g.run = &runBinding{config: cfg, ledger: ledger}
	return nil
}

func (g *Gateway) validateRunSnapshot(snapshot *provisioning.ClockSnapshot) error {
	if g.run == nil {
		return nil
	}
	context, err := g.run.ledger.Current()
	if err != nil {
		return err
	}
	if context.ProvisioningRevision != snapshot.Revision() {
		return errors.New("run context revision does not match snapshot")
	}
	for _, clock := range snapshot.Native().Clocks {
		if clock.ClockID == g.run.config.ClockID {
			return nil
		}
	}
	return errors.New("run clock is absent from snapshot")
}

// Record receiver activation while the verified producer is still paused.
func (g *Gateway) activateRunSession(record *sessionRecord) error {
	if g.run == nil {
		return nil
	}
	now := time.Now().UTC()
	if err := g.run.ledger.Apply(now); err != nil {
		return err
	}
	if err := g.run.ledger.Archive(filepath.Join(g.store.dir, "run-context", record.Session+".json"), now); err != nil {
		return err
	}
	record.RunClockID = g.run.config.ClockID
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err = atomicFile(filepath.Join(g.store.dir, "sessions", record.Session+".json"), raw); err != nil {
		return err
	}
	g.store.mu.Lock()
	g.store.records[record.Session] = record
	g.store.mu.Unlock()
	return nil
}

func (g *Gateway) bindRunFrame(msg jetstream.Msg, record *sessionRecord, frame *pb.TelemetryFrame) (*pb.TelemetryFrame, error) {
	if record.RunClockID == "" || frame.Attributes["sim_clock_id"] != record.RunClockID {
		return frame, nil
	}
	if g.run == nil || record.RunClockID != g.run.config.ClockID {
		return nil, errors.New("historical run verifier unavailable")
	}
	startRaw, endRaw := msg.Headers().Get("Nexus-Poll-Started-At"), msg.Headers().Get("Nexus-Poll-Ended-At")
	start, err := time.Parse(time.RFC3339Nano, startRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: missing/invalid poll start", errInvalidPoll)
	}
	end, err := time.Parse(time.RFC3339Nano, endRaw)
	if err != nil {
		return nil, fmt.Errorf("%w: missing/invalid poll end", errInvalidPoll)
	}
	observed, err := time.Parse(time.RFC3339Nano, frame.Timestamp)
	if err != nil || end.Before(start) || observed.Before(start) || observed.After(end) || end.After(time.Now().UTC()) {
		return nil, fmt.Errorf("%w: invalid acquisition interval", errInvalidPoll)
	}
	identity, _ := json.Marshal(struct {
		Session, Start, End string
		Raw                 []byte
	}{record.Session, startRaw, endRaw, msg.Data()})
	digest := fmt.Sprintf("%x", sha256.Sum256(identity))
	receipt := filepath.Join(g.store.dir, "run-frames", digest+".pb")
	if raw, err := os.ReadFile(receipt); err == nil {
		var original pb.TelemetryFrame
		if err = proto.Unmarshal(raw, &original); err != nil {
			return nil, err
		}
		return &original, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	archive, err := rc.Open(filepath.Join(g.store.dir, "run-context", record.Session+".json"), rc.Scope{GatewayID: g.store.scope.GatewayID, ClockID: record.RunClockID}, g.run.config.PublicKey)
	if err != nil {
		return nil, err
	}
	context, err := archive.Current()
	if err != nil {
		return nil, err
	}
	if frame.Attributes["sim_time_quality"] == "reset" {
		envelope, err := archive.SignedEnvelope()
		if err != nil {
			return nil, err
		}
		if err = g.run.ledger.InvalidateContext(envelope); err != nil {
			return nil, err
		}
	}
	result := archive.Evaluate(record.snapshot.Revision(), start, end)
	invalidated, err := g.run.ledger.Invalidated(context.RunID)
	if err != nil {
		return nil, err
	}
	current, err := g.run.ledger.Current()
	if err != nil {
		return nil, err
	}
	if invalidated {
		result = rc.Result{Status: "invalid", Reason: "reset_detected"}
	} else if current.RunID != context.RunID {
		// No acquisition classification survived for this older run. Preserve known
		// receipts above, but never manufacture a historical valid verdict now.
		result = rc.Result{Status: "unverified", Reason: "generation_history_unavailable"}
	}
	committed, applied, err := archive.ReceiverTimes()
	if err != nil {
		return nil, err
	}
	frame.Attributes["sim_context_committed_at"] = committed.Format(time.RFC3339Nano)
	if applied != nil {
		frame.Attributes["sim_context_applied_at"] = applied.Format(time.RFC3339Nano)
	}
	frame.Attributes["sim_poll_started_at"] = startRaw
	frame.Attributes["sim_poll_ended_at"] = endRaw
	frame.Attributes["sim_run_status"] = result.Status
	delete(frame.Attributes, "sim_run_reason")
	delete(frame.Attributes, "sim_run_id")
	delete(frame.Attributes, "sim_context_generation")
	if result.Reason != "" {
		frame.Attributes["sim_run_reason"] = result.Reason
	}
	if result.Status == "valid" {
		frame.Attributes["sim_run_id"] = result.RunID
		frame.Attributes["sim_context_generation"] = strconv.FormatUint(result.Generation, 10)
	}
	raw, err := proto.Marshal(frame)
	if err != nil {
		return nil, err
	}
	// Commit the classification BEFORE buffering/ACK: replay is byte-stable even
	// after expiry/reset/run replacement and does not use today's lease.
	if err = atomicFile(receipt, raw); err != nil {
		return nil, err
	}
	return frame, nil
}
