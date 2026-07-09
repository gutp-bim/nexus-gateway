// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
		ok   bool
	}{
		{"", slog.LevelInfo, true}, // default
		{"info", slog.LevelInfo, true},
		{"INFO", slog.LevelInfo, true}, // case-insensitive
		{" debug ", slog.LevelDebug, true},
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"trace", 0, false}, // unknown → error
		{"5", 0, false},
	}
	for _, c := range cases {
		got, err := parseLevel(c.in)
		if c.ok && err != nil {
			t.Errorf("parseLevel(%q) unexpected error: %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("parseLevel(%q) expected an error, got level %v", c.in, got)
		}
		if c.ok && got != c.want {
			t.Errorf("parseLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBuild_JSONFormatProducesParseableLines(t *testing.T) {
	var buf bytes.Buffer
	h, err := build("info", "json", &buf)
	if err != nil {
		t.Fatalf("build json: %v", err)
	}
	slog.New(h).Info("hello", "k", "v")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("LOG_FORMAT=json did not produce a parseable JSON line: %v (%q)", err, buf.String())
	}
	if m["msg"] != "hello" || m["k"] != "v" {
		t.Fatalf("json line missing fields: %v", m)
	}
}

func TestBuild_DebugLevelEnablesDebugSite(t *testing.T) {
	var buf bytes.Buffer
	h, err := build("debug", "text", &buf)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	slog.New(h).Debug("verbose")
	if !strings.Contains(buf.String(), "verbose") {
		t.Fatalf("LOG_LEVEL=debug did not enable the debug site; got %q", buf.String())
	}
}

func TestBuild_InfoLevelSuppressesDebug(t *testing.T) {
	var buf bytes.Buffer
	h, _ := build("info", "text", &buf)
	slog.New(h).Debug("should-not-appear")
	if buf.Len() != 0 {
		t.Fatalf("info level should suppress debug, got %q", buf.String())
	}
}

func TestBuild_InvalidFormatFailsFast(t *testing.T) {
	if _, err := build("info", "yaml", &bytes.Buffer{}); err == nil {
		t.Fatal("build with an invalid LOG_FORMAT should return an error")
	}
}

func TestResolved_NormalizesToCanonicalNames(t *testing.T) {
	t.Setenv("LOG_LEVEL", "WARNING") // accepted alias for warn, arbitrary casing
	t.Setenv("LOG_FORMAT", "JSON")
	level, format := Resolved()
	if level != "warn" {
		t.Errorf("Resolved level = %q, want canonical warn", level)
	}
	if format != "json" {
		t.Errorf("Resolved format = %q, want canonical json", format)
	}
}

func TestResolved_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	level, format := Resolved()
	if level != "info" || format != "text" {
		t.Errorf("Resolved() = (%q,%q), want (info,text)", level, format)
	}
}
