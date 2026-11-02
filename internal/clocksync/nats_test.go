package clocksync

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	pb "nexus-gateway/gen"
	"nexus-gateway/internal/dispatch"
)

func clockNATSUsers() []*server.User {
	runtime := "cfg.bacnet." + scope.ConnectorID + ".runtime"
	command := "cmd.bacnet." + scope.ConnectorID
	return []*server.User{
		{Username: "core", Password: "synthetic-core-202", Permissions: &server.Permissions{
			Publish:   &server.SubjectPermission{Allow: []string{"$JS.API.>", "$JS.ACK.>", runtime, command}},
			Subscribe: &server.SubjectPermission{Allow: []string{"_INBOX.core.>"}},
		}},
		{Username: "producer", Password: "synthetic-producer-202", Permissions: &server.Permissions{
			Publish:   &server.SubjectPermission{Allow: []string{"evt.bacnet." + scope.ConnectorID}},
			Subscribe: &server.SubjectPermission{Allow: []string{runtime, command, "_INBOX." + scope.ConnectorID + ".>"}},
			Response:  &server.ResponsePermission{MaxMsgs: 1, Expires: time.Minute},
		}},
	}
}

func TestGatewayInvalidProducerCredentialsNeverOpenAdmission(t *testing.T) {
	for _, failure := range []string{"missing", "malformed", "wrong"} {
		t.Run(failure, func(t *testing.T) {
			g, c, d, _, _, _, _, _ := liveFixtureAuthenticated(t, true, true)
			auth, err := ConnectorNATSAuthFile(g.producer.base)
			require.NoError(t, err)
			switch failure {
			case "missing":
				for i, entry := range g.producer.base.Env {
					if strings.HasPrefix(entry, "NATS_AUTH_FILE=") {
						g.producer.base.Env[i] = "NATS_AUTH_FILE=" + auth + ".absent"
					}
				}
			case "malformed":
				require.NoError(t, os.WriteFile(auth, []byte(`{"password":"synthetic-failure-202",bad}`), 0600))
			case "wrong":
				require.NoError(t, os.WriteFile(auth, []byte(`{"user":"producer","password":"synthetic-failure-202"}`), 0600))
			}
			require.Error(t, c.Sync(t.Context()))
			require.Empty(t, c.AppliedRevision())
			_, admitted := g.Enter(t.Context())
			require.False(t, admitted)
			d.mu.Lock()
			running := d.running
			d.mu.Unlock()
			require.False(t, running)
			status, err := json.Marshal(c.Status())
			require.NoError(t, err)
			require.NotContains(t, string(status), "synthetic-failure-202")
			logs, err := os.ReadFile(d.stderr.Name())
			require.NoError(t, err)
			require.NotContains(t, string(logs), "synthetic-failure-202")
		})
	}
}

func TestGatewayAuthenticatedNATSIsolationAndRollback(t *testing.T) {
	g, c, d, src, _, _, resolver, _ := liveFixtureAuthenticated(t, true, true)
	require.NoError(t, c.Sync(t.Context()))
	require.Eventually(t, func() bool { return g.buffer.Written() >= 35 }, 3*time.Second, 10*time.Millisecond)
	command := dispatch.New(g.nc, resolver, time.Second).WithAdmission(g)
	require.True(t, command.Execute(t.Context(), &pb.ControlCommand{ControlId: "authenticated", PointId: "BPT-0033", PresentValue: 0.5}).Success)
	d.mu.Lock()
	env := strings.Join(d.config.Env, "\n")
	binds := append([]string(nil), d.hostConfig.Binds...)
	d.mu.Unlock()
	require.NotContains(t, env, "synthetic-core-202")
	require.NotContains(t, env, "synthetic-producer-202")
	auth, err := ConnectorNATSAuthFile(g.producer.base)
	require.NoError(t, err)
	require.Contains(t, binds, auth+":"+auth+":ro")
	status, err := json.Marshal(g.producer.base)
	require.NoError(t, err)
	require.NotContains(t, string(status), "synthetic-core-202")
	require.NotContains(t, string(status), "synthetic-producer-202")

	address := "nats://" + g.nc.ConnectedAddr()
	for _, options := range [][]nats.Option{nil, {nats.UserInfo("producer", "wrong")}} {
		conn, err := nats.Connect(address, append(options, nats.NoReconnect(), nats.Timeout(time.Second))...)
		if conn != nil {
			conn.Close()
		}
		require.ErrorIs(t, err, nats.ErrAuthorization)
	}
	// Real broker authorization errors prove the denied messages were rejected.
	for _, principal := range []struct {
		user, password, inbox string
		denied                []string
	}{
		{"producer", "synthetic-producer-202", "_INBOX." + scope.ConnectorID, []string{"cfg.bacnet." + scope.ConnectorID + ".runtime", "$JS.API.STREAM.CREATE.FORBIDDEN", "$JS.API.STREAM.NAMES", "evt.bacnet.other", "_INBOX.core.unsolicited"}},
		{"core", "synthetic-core-202", "_INBOX.core", []string{"evt.bacnet." + scope.ConnectorID}},
	} {
		errors := make(chan error, len(principal.denied))
		conn, err := nats.Connect(address, nats.UserInfo(principal.user, principal.password), nats.CustomInboxPrefix(principal.inbox), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { errors <- err }))
		require.NoError(t, err)
		t.Cleanup(conn.Close)
		for _, subject := range principal.denied {
			require.NoError(t, conn.Publish(subject, []byte("{}")))
			require.NoError(t, conn.FlushTimeout(time.Second))
			select {
			case err := <-errors:
				require.ErrorIs(t, err, nats.ErrPermissionViolation)
			case <-time.After(time.Second):
				t.Fatal("broker did not reject forbidden publication")
			}
		}
	}
	// A partially started authenticated candidate is stopped, then the previous
	// snapshot is restored using a new producer with the same restricted identity.
	old := src.s
	_, src.s = snapshots(t)
	d.mu.Lock()
	d.failStart = 1
	d.mu.Unlock()
	require.Error(t, c.Sync(t.Context()))
	require.Equal(t, old.Revision(), c.AppliedRevision())
	require.Equal(t, old.Entries(), resolver.Snapshot())
	require.NoError(t, c.Sync(t.Context()))
	require.Equal(t, src.s.Revision(), c.AppliedRevision())
}
