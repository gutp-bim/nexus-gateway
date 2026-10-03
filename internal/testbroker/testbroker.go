// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Package testbroker starts a real Mosquitto broker for tests.
//
// The MQTT connector's tests used an in-process Go broker (mochi-mqtt). Its
// Inflight lock re-enters a read lock, so a burst of QoS 1 traffic could deadlock
// the broker itself and hang the suite for the 10 min test timeout. Mosquitto is
// also what the connector talks to in production, which makes the tests more
// faithful.
//
// Tests need the mosquitto binary on PATH (apt-get install mosquitto). Without
// it a test is skipped, unless NEXUS_REQUIRE_MOSQUITTO is set (CI sets it) — then
// the test fails, so a missing broker can never turn into silently skipped tests.
package testbroker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// RequireEnv, when set to any non-empty value, turns a missing mosquitto binary
// from a skip into a failure.
const RequireEnv = "NEXUS_REQUIRE_MOSQUITTO"

const (
	startAttempts = 3 // a free port can be taken between probing and mosquitto binding it
	readyTimeout  = 5 * time.Second
	stopTimeout   = 3 * time.Second
)

type config struct {
	denySubscribe []string
}

// Option customises the broker.
type Option func(*config)

// DenySubscribe makes the broker reject a SUBSCRIBE for exactly filter (SUBACK
// failure), which a client library surfaces as an error; everything else stays
// allowed, publishing included.
//
// Mosquitto's plain acl_file is not consulted for subscriptions, so this loads the
// dynamic-security plugin that ships with the mosquitto package. The plugin is
// looked for in the usual library directories, or at $NEXUS_MOSQUITTO_DYNSEC.
func DenySubscribe(filter string) Option {
	return func(c *config) { c.denySubscribe = append(c.denySubscribe, filter) }
}

// Start launches a Mosquitto broker on a free loopback port, allowing anonymous
// clients, and returns its "127.0.0.1:port" address. The broker is stopped when
// the test ends; its stderr is shown if the test failed.
func Start(t testing.TB, opts ...Option) string {
	t.Helper()
	bin, err := exec.LookPath("mosquitto")
	if err != nil {
		if os.Getenv(RequireEnv) != "" {
			t.Fatalf("mosquitto not found on PATH and %s is set: %v (install it: apt-get install mosquitto)", RequireEnv, err)
		}
		t.Skipf("mosquitto not found on PATH; install it to run this test (apt-get install mosquitto), or set %s=1 to make this an error", RequireEnv)
	}

	var cfg config
	for _, o := range opts {
		o(&cfg)
	}

	var lastErr error
	for range startAttempts {
		addr, stop, err := launch(t, bin, cfg)
		if err == nil {
			t.Cleanup(stop)
			return addr
		}
		lastErr = err
	}
	t.Fatalf("could not start mosquitto: %v", lastErr)
	return ""
}

// launch starts one broker attempt. On success it returns the address and a stop
// function; on failure the process is already gone.
func launch(t testing.TB, bin string, cfg config) (addr string, stop func(), err error) {
	t.Helper()
	port, err := freePort()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "testbroker-")
	if err != nil {
		return "", nil, err
	}
	// Every failure path below removes the directory through this one defer; on
	// success the returned stop function owns it.
	launched := false
	defer func() {
		if !launched {
			_ = os.RemoveAll(dir)
		}
	}()
	// The files hold no secrets. When the tests run as root, mosquitto drops to its
	// own user before reading them, so they must be readable by others.
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", nil, err
	}

	conf := fmt.Sprintf("listener %d 127.0.0.1\nallow_anonymous true\npersistence false\nlog_type error\nlog_type warning\n", port)
	if len(cfg.denySubscribe) > 0 {
		plugin, err := findDynsecPlugin()
		if err != nil {
			return "", nil, err
		}
		dynsecPath := filepath.Join(dir, "dynamic-security.json")
		if err := os.WriteFile(dynsecPath, dynsecConfig(cfg.denySubscribe), 0o644); err != nil {
			return "", nil, err
		}
		conf += "plugin " + plugin + "\nplugin_opt_config_file " + dynsecPath + "\n"
	}
	confPath := filepath.Join(dir, "mosquitto.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		return "", nil, err
	}

	var out lockedBuffer
	cmd := exec.Command(bin, "-c", confPath)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	stop = func() {
		select {
		case <-exited:
		default:
			// Ask nicely, then insist: Close must never be able to hang a test.
			if cmd.Process.Signal(os.Interrupt) != nil {
				_ = cmd.Process.Kill()
			}
			select {
			case <-exited:
			case <-time.After(stopTimeout):
				_ = cmd.Process.Kill()
				<-exited
			}
		}
		if t.Failed() {
			t.Logf("mosquitto output:\n%s", out.String())
		}
		_ = os.RemoveAll(dir)
	}

	addr = fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-exited: // e.g. address already in use
			return "", nil, fmt.Errorf("mosquitto exited during startup: %s", out.String())
		default:
		}
		if c, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond); derr == nil {
			_ = c.Close()
			launched = true
			return addr, stop, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	return "", nil, fmt.Errorf("mosquitto did not accept connections within %s: %s", readyTimeout, out.String())
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// lockedBuffer collects the broker's output; exec writes to it from its own
// goroutines while the test may read it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// dynsecConfig is a dynamic-security configuration under which anonymous clients
// may do everything except subscribe to the listed literal filters.
func dynsecConfig(denied []string) []byte {
	type acl struct {
		ACLType  string `json:"acltype"`
		Topic    string `json:"topic,omitempty"`
		Allow    bool   `json:"allow"`
		Priority int    `json:"priority"`
	}
	// Allow everything through the role itself (priority 0) rather than relying on
	// the plugin's default access, then deny the listed filters at a higher priority.
	// Mosquitto 2.0 names the publish ACL types publishClientSend/publishClientReceive;
	// later releases call them publishClientToBroker/publishBrokerToClient. A type the
	// plugin does not know is ignored, so both spellings are listed.
	acls := []acl{
		{ACLType: "publishClientSend", Topic: "#", Allow: true},
		{ACLType: "publishClientReceive", Topic: "#", Allow: true},
		{ACLType: "publishClientToBroker", Topic: "#", Allow: true},
		{ACLType: "publishBrokerToClient", Topic: "#", Allow: true},
		{ACLType: "subscribePattern", Topic: "#", Allow: true},
		{ACLType: "unsubscribePattern", Topic: "#", Allow: true},
	}
	for _, f := range denied {
		acls = append(acls, acl{ACLType: "subscribeLiteral", Topic: f, Allow: false, Priority: 1})
	}
	b, _ := json.MarshalIndent(map[string]any{
		"anonymousGroup": "anon",
		"clients":        []any{},
		"groups":         []any{map[string]any{"groupname": "anon", "roles": []any{map[string]any{"rolename": "deny-subscribe"}}}},
		"roles":          []any{map[string]any{"rolename": "deny-subscribe", "acls": acls}},
	}, "", "  ")
	return b
}

// findDynsecPlugin locates mosquitto's dynamic-security plugin.
func findDynsecPlugin() (string, error) {
	if p := os.Getenv("NEXUS_MOSQUITTO_DYNSEC"); p != "" {
		return p, nil
	}
	const name = "mosquitto_dynamic_security.so"
	dirs := []string{
		"/usr/lib/x86_64-linux-gnu", "/usr/lib/aarch64-linux-gnu", "/usr/lib64", "/usr/lib",
		"/usr/local/lib", "/opt/homebrew/lib", "/opt/homebrew/opt/mosquitto/lib", "/usr/local/opt/mosquitto/lib",
	}
	for _, d := range dirs {
		if p := filepath.Join(d, name); fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("mosquitto dynamic-security plugin (%s) not found; set NEXUS_MOSQUITTO_DYNSEC to its path", name)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
