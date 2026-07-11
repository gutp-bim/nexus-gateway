// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// Recorder is a bounded, thread-safe ring buffer of the most recent gateway log
// lines. It backs the Admin API's gateway-log source (#42): the gateway captures
// its own logs (as JSON lines, so severity is a structured field) in addition to
// writing them to stderr, without touching disk.
type Recorder struct {
	mu   sync.Mutex
	buf  []string
	cap  int
	next int
	full bool
}

// NewRecorder returns a Recorder holding at most capacity lines (default 500).
func NewRecorder(capacity int) *Recorder {
	if capacity <= 0 {
		capacity = 500
	}
	return &Recorder{buf: make([]string, capacity), cap: capacity}
}

// Write implements io.Writer so a slog JSON handler can feed it; each Handle
// emits exactly one line (record + trailing newline), which becomes one entry.
func (r *Recorder) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		r.add(line)
	}
	return len(p), nil
}

func (r *Recorder) add(line string) {
	r.mu.Lock()
	r.buf[r.next] = line
	r.next = (r.next + 1) % r.cap
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// Lines returns up to tail most-recent lines in chronological order (oldest
// first). tail <= 0 returns all buffered lines.
func (r *Recorder) Lines(tail int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var ordered []string
	if r.full {
		ordered = make([]string, 0, r.cap)
		ordered = append(ordered, r.buf[r.next:]...)
		ordered = append(ordered, r.buf[:r.next]...)
	} else {
		ordered = append(ordered, r.buf[:r.next]...)
	}
	if tail > 0 && tail < len(ordered) {
		ordered = ordered[len(ordered)-tail:]
	}
	// Return a copy so callers cannot mutate the shared slice's backing array.
	out := make([]string, len(ordered))
	copy(out, ordered)
	return out
}

// fanoutHandler dispatches each record to several slog handlers (e.g. the stderr
// handler and the JSON recorder), so gateway logs reach both without the record
// being formatted twice by the caller.
type fanoutHandler struct {
	handlers []slog.Handler
}

func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanoutHandler) Handle(ctx context.Context, rec slog.Record) error {
	for _, h := range f.handlers {
		if h.Enabled(ctx, rec.Level) {
			// Clone so a handler that retains attrs cannot corrupt the next one.
			_ = h.Handle(ctx, rec.Clone())
		}
	}
	return nil
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return fanoutHandler{handlers: next}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return fanoutHandler{handlers: next}
}
