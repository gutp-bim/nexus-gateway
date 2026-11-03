// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package clocksync

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"nexus-gateway/internal/provisioning"
)

var sessionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type sessionRecord struct {
	RunClockID string          `json:"runClockId,omitempty"`
	Session    string          `json:"session"`
	Snapshot   json.RawMessage `json:"snapshot"`
	snapshot   *provisioning.ClockSnapshot
	nativeHash string
	verified   bool
}

type sessions struct {
	dir     string
	scope   provisioning.ClockScope
	mu      sync.RWMutex
	records map[string]*sessionRecord
}

func openSessions(dir string, scope provisioning.ClockScope) (*sessions, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("clock state directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	for _, sub := range []string{"sessions", "native", "quarantine"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			return nil, err
		}
	}
	// Only this subdirectory is mounted into the unprivileged connector. The
	// Core-owned parent and full canonical snapshot journal remain private.
	if err := os.Chmod(filepath.Join(dir, "native"), 0755); err != nil {
		return nil, err
	}
	s := &sessions{dir: dir, scope: scope, records: map[string]*sessionRecord{}}
	files, err := filepath.Glob(filepath.Join(dir, "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var r sessionRecord
		if err = json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		if !sessionPattern.MatchString(r.Session) || filepath.Base(file) != r.Session+".json" {
			return nil, fmt.Errorf("invalid archived session identity")
		}
		r.snapshot, err = provisioning.StageClock(r.Snapshot, scope)
		if err != nil {
			return nil, err
		}
		native, err := os.ReadFile(filepath.Join(dir, "native", r.Session+".json"))
		if err != nil {
			return nil, err
		}
		expected, err := json.Marshal(r.snapshot.Native())
		if err != nil {
			return nil, err
		}
		if string(native) != string(expected) {
			return nil, fmt.Errorf("archived native bundle mismatch")
		}
		if r.RunClockID != "" {
			found := false
			for _, clock := range r.snapshot.Native().Clocks {
				if clock.ClockID == r.RunClockID {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("archived run clock missing from snapshot")
			}
			if _, err = os.Stat(filepath.Join(dir, "run-context", r.Session+".json")); err != nil {
				return nil, fmt.Errorf("run session history unavailable: %w", err)
			}
		}
		r.nativeHash = fmt.Sprintf("%x", sha256.Sum256(native))
		proof, err := os.ReadFile(file + ".verified")
		if err == nil {
			if string(proof) != r.nativeHash {
				return nil, fmt.Errorf("invalid archived runtime receipt")
			}
			r.verified = true
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		s.records[r.Session] = &r
	}
	return s, nil
}

func (s *sessions) create(snapshot *provisioning.ClockSnapshot) (*sessionRecord, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(random[:])
	native, err := json.Marshal(snapshot.Native())
	if err != nil {
		return nil, err
	}
	r := &sessionRecord{Session: id, Snapshot: snapshot.Bytes(), snapshot: snapshot, nativeHash: fmt.Sprintf("%x", sha256.Sum256(native))}
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if err = atomicFileMode(filepath.Join(s.dir, "native", id+".json"), native, 0444); err != nil {
		return nil, err
	}
	if err = atomicFile(filepath.Join(s.dir, "sessions", id+".json"), data); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.records[id] = r
	s.mu.Unlock()
	return r, nil
}
func (s *sessions) get(id string) (*sessionRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.records[id]
	if !ok {
		return nil, false
	}
	copy := *r
	return &copy, true
}
func (s *sessions) verify(r *sessionRecord) error {
	if err := atomicFile(filepath.Join(s.dir, "sessions", r.Session+".json.verified"), []byte(r.nativeHash)); err != nil {
		return err
	}
	s.mu.Lock()
	s.records[r.Session].verified = true
	s.mu.Unlock()
	return nil
}
func (s *sessions) commit(r *sessionRecord) error {
	return atomicFile(filepath.Join(s.dir, "active.json"), []byte(r.Session))
}
func (s *sessions) lastGood() (*provisioning.ClockSnapshot, error) {
	id, err := os.ReadFile(filepath.Join(s.dir, "active.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, ok := s.get(string(id))
	if !ok || !r.verified {
		return nil, fmt.Errorf("invalid last-good session journal")
	}
	return r.snapshot, nil
}

// Files are immutable per session; no archive GC can invalidate late replay.
// Atomic rename and directory fsync make the binding durable BEFORE Start.
func atomicFile(path string, data []byte) error {
	return atomicFileMode(path, data, 0600)
}

func atomicFileMode(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".stage-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
