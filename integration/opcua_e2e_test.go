// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_OpcUATelemetry verifies that the OPC-UA connector reads PT001..PT008 from
// opcua-sim-gateway and publishes Common Events with native addressing only (ADR-0001).
//
// Prerequisite: start the integration stack with the opcua profile:
//
//	docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile opcua up -d
//
// Then run:
//
//	E2E_NATS_URL=nats://localhost:14222 go test ./integration/... -run TestE2E_OpcUATelemetry -v -timeout 120s
//
// The test skips automatically when E2E_NATS_URL is unset (normal unit/in-process CI run).
func TestE2E_OpcUATelemetry(t *testing.T) {
	natsURL := os.Getenv("E2E_NATS_URL")
	if natsURL == "" {
		t.Skip("E2E_NATS_URL not set — start the integration stack and set E2E_NATS_URL to run")
	}

	nc, err := nats.Connect(natsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(15),
		nats.ReconnectWait(2*time.Second),
	)
	require.NoError(t, err, "connect to NATS at %s", natsURL)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	// Wait for the EVENTS stream (provisioned by the gateway at startup).
	var cons jetstream.Consumer
	require.Eventually(t, func() bool {
		var cerr error
		cons, cerr = js.CreateOrUpdateConsumer(t.Context(), "EVENTS", jetstream.ConsumerConfig{
			FilterSubject: "evt.opcua.opcua-01",
			AckPolicy:     jetstream.AckNonePolicy,
			DeliverPolicy: jetstream.DeliverAllPolicy,
		})
		return cerr == nil
	}, 30*time.Second, 2*time.Second, "EVENTS stream did not appear within 30 s (is the gateway running?)")

	// Collect frames. The connector polls every 5 s so at most two poll cycles
	// are needed to observe all 8 points.
	const wantPoints = 8
	seen := make(map[string]struct{})

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && len(seen) < wantPoints {
		msgs, ferr := cons.Fetch(wantPoints, jetstream.FetchMaxWait(10*time.Second))
		if ferr != nil {
			continue
		}
		for msg := range msgs.Messages() {
			var evt map[string]any
			if jerr := json.Unmarshal(msg.Data(), &evt); jerr != nil {
				continue
			}
			// ADR-0001: connector publishes native addressing only.
			assert.Equal(t, "opcua", evt["protocol"])
			assert.Equal(t, "opcua-01", evt["connector_id"])
			assert.NotContains(t, evt, "point_id",
				"ADR-0001: canonical point_id must not appear in the connector event")
			localID, _ := evt["local_id"].(string)
			assert.True(t, strings.HasPrefix(localID, "ns=2;s=PT"),
				"local_id must be ns=2;s=PTxxx, got %q", localID)
			seen[localID] = struct{}{}
		}
	}

	assert.Len(t, seen, wantPoints,
		"expected %d distinct OPC-UA points in EVENTS stream, got %d: %v",
		wantPoints, len(seen), seen)
	t.Logf("OPC-UA E2E: %d/8 points: %v", len(seen), seen)

	// Phase 2 (MVP scenario #2): prove the telemetry traversed
	// Normalizer → SQLite Store-and-Forward → mock Building OS, not just NATS.
	// Scrape the gateway's unauthenticated /metrics and require the store-and-forward
	// counters to show frames both written to the buffer and sent up (#73, #74).
	adminURL := os.Getenv("E2E_ADMIN_URL")
	if adminURL == "" {
		adminURL = "http://localhost:18080"
	}
	var written, sent int64
	require.Eventually(t, func() bool {
		body, err := scrapeMetrics(adminURL)
		if err != nil {
			return false
		}
		written = parseCounter(body, "storefwd_written_total")
		sent = parseCounter(body, "storefwd_sent_total")
		return written > 0 && sent > 0
	}, 60*time.Second, 2*time.Second,
		"store-and-forward /metrics must show frames written to the buffer and sent to Building OS")
	t.Logf("OPC-UA E2E: store-and-forward written=%d sent=%d (reached mock Building OS)", written, sent)
}

// scrapeMetrics fetches the gateway's Prometheus /metrics text (unauthenticated).
func scrapeMetrics(baseURL string) (string, error) {
	resp, err := http.Get(strings.TrimRight(baseURL, "/") + "/metrics")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("/metrics: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// parseCounter returns the value of an unlabeled Prometheus counter line
// ("<name> <value>"), or 0 if absent.
func parseCounter(body, name string) int64 {
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(line, name+" "); ok {
			var v int64
			if _, err := fmt.Sscanf(strings.TrimSpace(rest), "%d", &v); err == nil {
				return v
			}
		}
	}
	return 0
}
