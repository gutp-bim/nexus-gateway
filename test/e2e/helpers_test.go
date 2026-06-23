// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	pb "nexus-gateway/gen"
)

// requireEnv skips the test unless the named environment variable is set,
// returning its value. This is how a scaffold stays green-by-skipping until the
// live environment is wired up.
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("set %s to run this E2E test (requires a live stack)", key)
	}
	return v
}

// connectNATS dials the NATS URL from E2E_NATS_URL (or skips).
func connectNATS(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	url := requireEnv(t, "E2E_NATS_URL")
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect NATS %s: %v", url, err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	return nc, js
}

// awaitCommonEvent consumes evt.<protocol>.<connectorID> on the EVENTS stream and
// returns the first event whose local_id is in want, failing on timeout. Proves a
// connector is publishing native-addressed Common Events from real device reads.
func awaitCommonEvent(t *testing.T, js jetstream.JetStream, protocol, connectorID string, want map[string]bool, timeout time.Duration) commonEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	subject := "evt." + protocol + "." + connectorID
	cons, err := js.CreateOrUpdateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create consumer on %s: %v", subject, err)
	}
	for ctx.Err() == nil {
		msgs, err := cons.Fetch(16, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			continue
		}
		for msg := range msgs.Messages() {
			_ = msg.Ack()
			var ev commonEvent
			if json.Unmarshal(msg.Data(), &ev) == nil && want[ev.LocalID] {
				return ev
			}
		}
	}
	t.Fatalf("no Common Event on %s for any of %v within %s", subject, keys(want), timeout)
	return commonEvent{}
}

// commonEvent mirrors the connector wire format (internal/common.Event) without
// importing it, since the e2e package only observes the wire.
type commonEvent struct {
	Protocol    string  `json:"protocol"`
	ConnectorID string  `json:"connector_id"`
	LocalID     string  `json:"local_id"`
	DeviceRef   string  `json:"device_ref"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	Quality     string  `json:"quality"`
	Timestamp   string  `json:"timestamp"`
}

// writeReply mirrors the connector control reply.
type writeReply struct {
	Success  bool   `json:"success"`
	Response string `json:"response"`
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// _ keeps the generated proto import referenced for future frame-level assertions
// against the mock Building OS (e.g. decoding TelemetryFrame at the ingress).
var _ = pb.TelemetryFrame{}
