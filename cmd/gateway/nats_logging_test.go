package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
)

// Run the real main in a subprocess so its startup logs and os.Exit paths are
// covered without changing the parent test's flags, logger or environment.
func TestGatewayStartupNATSRedaction(t *testing.T) {
	if os.Getenv("NEXUS_LOG_CANARY_CHILD") == "1" {
		flag.CommandLine = flag.NewFlagSet("gateway", flag.ExitOnError)
		os.Args = []string{"gateway", "--bos-insecure", "--nats", os.Getenv("NATS_URL"), "--point-list", "/nonexistent/canary-point-list.json"}
		main()
		return
	}
	const canary = "synthetic-secret-203"
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, format := range []string{"text", "json"} {
		for _, kind := range []string{"password", "token", "malformed", "multiple"} {
			t.Run(format+"/"+kind, func(t *testing.T) {
				opts := &server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true}
				if kind == "token" {
					opts.Authorization = canary
				} else {
					opts.Username, opts.Password = "synthetic-user", canary
				}
				ns, err := server.NewServer(opts)
				require.NoError(t, err)
				go ns.Start()
				require.True(t, ns.ReadyForConnections(5*time.Second))
				t.Cleanup(ns.Shutdown)
				address := strings.Replace(ns.ClientURL(), "://", "://synthetic-user:"+canary+"@", 1)
				switch kind {
				case "token":
					address = strings.Replace(ns.ClientURL(), "://", "://"+canary+"@", 1)
				case "malformed":
					address = "nats://user:" + canary + "@invalid:%zz"
				case "multiple":
					address += "," + address
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestGatewayStartupNATSRedaction$")
				cmd.Env = []string{"NEXUS_LOG_CANARY_CHILD=1", "NATS_URL=" + address, "LOG_FORMAT=" + format, "POINT_LIST_PERSIST=" + filepath.Join(t.TempDir(), "points.json")}
				output, err := cmd.CombinedOutput()
				require.Error(t, err)
				require.NoError(t, ctx.Err(), "startup must exit without waiting for equipment")
				require.Contains(t, string(output), "resolved configuration")
				require.False(t, strings.Contains(string(output), canary), "startup output exposed synthetic credential")
			})
		}
	}
}
