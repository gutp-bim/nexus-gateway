// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRecorder_ChronologicalOrderAndTail(t *testing.T) {
	r := NewRecorder(3)
	for _, s := range []string{"a", "b"} {
		r.add(s)
	}
	if got := r.Lines(0); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("Lines(0) = %v, want [a b]", got)
	}
	if got := r.Lines(1); len(got) != 1 || got[0] != "b" {
		t.Fatalf("Lines(1) = %v, want [b]", got)
	}
}

func TestRecorder_WrapAroundKeepsMostRecent(t *testing.T) {
	r := NewRecorder(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		r.add(s)
	}
	// capacity 3 → only the last three survive, oldest first.
	got := r.Lines(0)
	if len(got) != 3 || got[0] != "c" || got[1] != "d" || got[2] != "e" {
		t.Fatalf("Lines after wrap = %v, want [c d e]", got)
	}
}

func TestRecorder_DefaultCapacityAndReturnsCopy(t *testing.T) {
	r := NewRecorder(0) // → default 500
	r.add("x")
	got := r.Lines(0)
	got[0] = "mutated"
	if r.Lines(0)[0] != "x" {
		t.Fatal("Lines must return a copy the caller cannot mutate")
	}
}

// TestRecorder_CapturesJSONWithLevel exercises the exact mechanism Setup uses: a
// slog JSON handler writing to the Recorder, so gateway-log lines carry a
// structured `level` the Logs screen can filter on.
func TestRecorder_CapturesJSONWithLevel(t *testing.T) {
	r := NewRecorder(10)
	log := slog.New(slog.NewJSONHandler(r, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Warn("disk near capacity", "pct", 91)

	lines := r.Lines(0)
	if len(lines) != 1 {
		t.Fatalf("want 1 captured line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], `"level":"WARN"`) {
		t.Fatalf("captured line missing structured level: %s", lines[0])
	}
	if !strings.Contains(lines[0], "disk near capacity") {
		t.Fatalf("captured line missing message: %s", lines[0])
	}
}

func TestFanoutHandler_WritesToBothSinks(t *testing.T) {
	var stderr bytes.Buffer
	rec := NewRecorder(10)
	base := slog.NewTextHandler(&stderr, nil)
	recH := slog.NewJSONHandler(rec, nil)
	log := slog.New(fanoutHandler{handlers: []slog.Handler{base, recH}})

	log.Info("hello", "k", "v")

	if !strings.Contains(stderr.String(), "hello") {
		t.Fatalf("stderr sink missing line: %q", stderr.String())
	}
	lines := rec.Lines(0)
	if len(lines) != 1 || !strings.Contains(lines[0], `"msg":"hello"`) {
		t.Fatalf("recorder sink missing JSON line: %v", lines)
	}
}
