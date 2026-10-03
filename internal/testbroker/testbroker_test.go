// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package testbroker

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// leftovers lists the broker temp directories under dir.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "testbroker-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func needMosquitto(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("mosquitto")
	if err != nil {
		if os.Getenv(RequireEnv) != "" {
			t.Fatalf("mosquitto not found and %s is set: %v", RequireEnv, err)
		}
		t.Skip("mosquitto not found")
	}
	return bin
}

// A failed start must not leak its temp directory, whichever step failed (review
// on #193). Here mosquitto is pointed at a plugin that does not exist, so it exits
// during startup after the config files were written.
func TestLaunch_FailedStartRemovesTempDir(t *testing.T) {
	bin := needMosquitto(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // os.MkdirTemp honours it, isolating this test's directories
	t.Setenv("NEXUS_MOSQUITTO_DYNSEC", filepath.Join(tmp, "no-such-plugin.so"))

	if _, _, err := launch(t, bin, config{denySubscribe: []string{"x/y"}}); err == nil {
		t.Fatal("launch with a missing plugin should fail")
	}
	if got := leftovers(t, tmp); len(got) != 0 {
		t.Fatalf("failed launch left temp dirs behind: %v", got)
	}
}

// A successful start hands the directory to stop, which removes it.
func TestLaunch_StopRemovesTempDir(t *testing.T) {
	bin := needMosquitto(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	addr, stop, err := launch(t, bin, config{})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if addr == "" {
		t.Fatal("no address")
	}
	if got := leftovers(t, tmp); len(got) != 1 {
		t.Fatalf("a running broker owns exactly one temp dir, got %v", got)
	}
	stop()
	if got := leftovers(t, tmp); len(got) != 0 {
		t.Fatalf("stop left temp dirs behind: %v", got)
	}
}
