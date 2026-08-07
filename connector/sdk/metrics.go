// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"fmt"
	"net/http"
)

// Metric is one counter/gauge series in the Prometheus text exposition. The
// gateway's Admin API writes the same format by hand (internal/adminapi), and
// connectors follow it so both surfaces scrape identically without pulling a
// Prometheus client library into every connector image.
type Metric struct {
	Name  string // bare snake_case series name, e.g. mqtt_received_total
	Help  string // one-line "# HELP" description
	Type  string // "# TYPE" value: counter or gauge
	Value int64
}

// MetricsHandler serves the Prometheus text exposition for the series returned
// by collect, which is called once per scrape. A nil collect serves an empty
// (but valid) body, so a connector without instrumentation still answers.
func MetricsHandler(collect func() []Metric) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if collect == nil {
			return
		}
		for _, m := range collect() {
			fmt.Fprintf(w, "# HELP %s %s\n", m.Name, m.Help)
			fmt.Fprintf(w, "# TYPE %s %s\n", m.Name, m.Type)
			fmt.Fprintf(w, "%s %d\n", m.Name, m.Value)
		}
	}
}
