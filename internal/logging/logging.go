// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package logging configures the process-wide structured logger from the
// environment, so operators can change verbosity and format without a rebuild
// (#25). It is intentionally env-driven (LOG_LEVEL / LOG_FORMAT) rather than
// flag-driven so it can be set up before flags are parsed and every subsequent
// slog call honors it.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// records is the process-wide gateway log ring buffer, populated by Setup and
// surfaced to the Admin API via Records() (#42). It is created even before Setup
// so an early caller never dereferences nil.
var records = NewRecorder(500)

// Records returns the gateway log recorder (the in-memory ring buffer of recent
// gateway log lines) for the Admin API's gateway-log source.
func Records() *Recorder { return records }

// Setup builds the default slog logger from LOG_LEVEL (debug/info/warn/error,
// default info) and LOG_FORMAT (text/json, default text) and installs it via
// slog.SetDefault. Every record is also captured (as a JSON line, so severity is
// a structured field) into the gateway log recorder (Records()) for the Admin
// API. Invalid values return an error so the caller can fail fast; the default
// logger remains usable to report that error.
func Setup() error {
	level, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return err
	}
	base, err := build(os.Getenv("LOG_LEVEL"), os.Getenv("LOG_FORMAT"), os.Stderr)
	if err != nil {
		return err
	}
	// The recorder always captures JSON (regardless of the stderr LOG_FORMAT) so
	// the gateway-log source has a structured `level` field to filter on.
	recorder := slog.NewJSONHandler(records, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(fanoutHandler{handlers: []slog.Handler{base, recorder}}))
	return nil
}

// build assembles a handler from raw level/format strings. Split out from Setup
// (which touches global state) so the parsing/validation is unit-testable.
func build(levelStr, formatStr string, w io.Writer) (slog.Handler, error) {
	level, err := parseLevel(levelStr)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: level}
	switch strings.ToLower(strings.TrimSpace(formatStr)) {
	case "", "text":
		return slog.NewTextHandler(w, opts), nil
	case "json":
		return slog.NewJSONHandler(w, opts), nil
	default:
		return nil, fmt.Errorf("invalid LOG_FORMAT %q (want text or json)", formatStr)
	}
}

// Resolved returns the canonical level and format the logger is configured
// with, for the startup config summary — so the log shows "warn"/"json" rather
// than whatever casing the operator typed. Safe to call after Setup (which has
// already validated the env); unset values report their defaults.
func Resolved() (level, format string) {
	level = "info"
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		level = "debug"
	case "warn", "warning":
		level = "warn"
	case "error":
		level = "error"
	}
	format = "text"
	if strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT"))) == "json" {
		format = "json"
	}
	return level, format
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid LOG_LEVEL %q (want debug, info, warn, or error)", s)
	}
}
