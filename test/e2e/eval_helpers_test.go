// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bufio"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scrapeGatewayMetrics fetches /metrics from the Admin API and extracts selected
// Prometheus counters/gauges. Returns zeroes (not a failure) when the endpoint is
// unavailable — callers should t.Log a warning. Metrics relevant to the paper
// evaluations are:
//
//	process_cpu_seconds_total     — gateway CPU accumulator; diff two samples for %
//	process_resident_memory_bytes — RSS in bytes
//	storefwd_buffer_depth         — current SQLite ring buffer occupancy
//	storefwd_sent_total           — frames delivered to Building OS
//	storefwd_dropped_total        — frames dropped (buffer full during outage)
//	normalizer_invalid_total      — events rejected as malformed
//	normalizer_unresolved_total   — events dropped due to point-list miss
func scrapeGatewayMetrics(t *testing.T, adminURL string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	resp, err := http.Get(adminURL + "/metrics")
	if err != nil {
		t.Logf("warn: GET %s/metrics: %v", adminURL, err)
		return out
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		// Handle label-bearing lines: strip {…} label set.
		name := line
		if idx := strings.IndexByte(line, '{'); idx >= 0 {
			name = line[:idx] + line[strings.IndexByte(line, '}')+1:]
		}
		parts := strings.Fields(name)
		if len(parts) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		out[parts[0]] = val
	}
	return out
}

// diffMetric returns metrics[key] after - before (for counters that accumulate).
func diffMetric(before, after map[string]float64, key string) float64 {
	return after[key] - before[key]
}

// percentileD returns the p-th percentile (0–100) of a slice of durations.
// The slice is sorted in place.
func percentileD(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := int(math.Ceil(float64(len(samples))*p/100)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

// parseIntListEnv reads a comma-separated list of ints from the given env var.
// Returns defaults unchanged when the var is unset or empty.
func parseIntListEnv(t *testing.T, key string, defaults []int) []int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return defaults
	}
	var out []int
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("env %s: %q is not an int: %v", key, s, err)
		}
		out = append(out, n)
	}
	return out
}

// parseIntEnv reads a single int from the given env var. Returns def when unset.
func parseIntEnv(t *testing.T, key string, def int) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		t.Fatalf("env %s: %v", key, err)
	}
	return n
}

// evalCSVRow formats a slice of any values as a CSV row string.
func evalCSVRow(vals ...any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ",")
}
