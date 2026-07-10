// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback, useEffect, useState } from "react";
import type { ConnectorItem, ConnectorLogs } from "@/lib/api";
import { apiFetch, isArrayOf, isRecord } from "@/lib/apiClient";
import { usePolling } from "@/lib/use-polling";
import { LastUpdated } from "@/components/last-updated";
import { ErrorBanner, messageFor } from "@/components/error-banner";

const TAIL_MS = 5_000;

export default function LogsPage() {
  const [connectors, setConnectors] = useState<ConnectorItem[]>([]);
  const [selectedID, setSelectedID] = useState<string>("");
  const [listError, setListError] = useState<string | null>(null);
  const [tailOn, setTailOn] = useState(false);

  // Load (or reload) the connector list. Keeps the current selection if there
  // is one, else selects the first connector.
  const loadConnectors = useCallback(() => {
    apiFetch<ConnectorItem[]>("/api/gateway/connectors", undefined, isArrayOf())
      .then((items) => {
        setConnectors(items);
        setListError(null);
        if (items.length > 0) setSelectedID((cur) => cur || items[0].id);
      })
      .catch((e) => setListError(messageFor(e)));
  }, []);

  useEffect(() => {
    loadConnectors();
  }, [loadConnectors]);

  // Poll the selected connector's log tail. The interval only runs while the
  // tail toggle is on (#41); selecting a connector always fetches once via the
  // effect below, regardless of the toggle. Selection is preserved across
  // refreshes (selectedID is never reset by a poll).
  const fetchLogs = useCallback((): Promise<ConnectorLogs | null> => {
    if (!selectedID) return Promise.resolve(null);
    return apiFetch<ConnectorLogs>(
      `/api/gateway/logs/${encodeURIComponent(selectedID)}?tail=200`,
      undefined,
      isRecord
    );
  }, [selectedID]);

  const { data: logs, error, fetching, lastUpdated, stale, refresh } = usePolling(fetchLogs, {
    intervalMs: TAIL_MS,
    enabled: tailOn && !!selectedID,
  });

  // Fetch once whenever the selected connector changes, independent of the tail toggle.
  useEffect(() => {
    if (selectedID) refresh();
  }, [selectedID, refresh]);

  // Retry both sources: a connector-list load failure must re-run that load (not
  // just the log poller), and a log failure re-polls the tail.
  const retry = useCallback(() => {
    loadConnectors();
    refresh();
  }, [loadConnectors, refresh]);

  const lineStyle = (line: string): React.CSSProperties => {
    const l = line.toLowerCase();
    if (l.includes("error") || l.includes("err ")) return { color: "#dc2626" };
    if (l.includes("warn")) return { color: "#d97706" };
    return { color: "#d1d5db" };
  };

  return (
    <div>
      <div style={{ display: "flex", alignItems: "center", gap: "1rem", marginBottom: "1rem", flexWrap: "wrap" }}>
        <h1 style={{ fontSize: "1.25rem", fontWeight: 700, margin: 0 }}>Connector Logs</h1>
        <select
          value={selectedID}
          onChange={(e) => setSelectedID(e.target.value)}
          style={{ padding: "0.3rem 0.6rem", borderRadius: "0.25rem", border: "1px solid #d1d5db", fontSize: "0.875rem" }}
        >
          {connectors.length === 0 && <option value="">No connectors</option>}
          {connectors.map((c) => (
            <option key={c.id} value={c.id}>{c.id} {c.running ? "●" : "○"}</option>
          ))}
        </select>
        <button
          onClick={() => refresh()}
          disabled={fetching || !selectedID}
          style={{
            padding: "0.3rem 0.75rem",
            fontSize: "0.875rem",
            border: "1px solid #d1d5db",
            borderRadius: "0.25rem",
            cursor: fetching ? "not-allowed" : "pointer",
            opacity: fetching ? 0.5 : 1,
          }}
        >
          {fetching ? "Loading…" : "Reload"}
        </button>
        <label style={{ display: "flex", alignItems: "center", gap: "0.35rem", fontSize: "0.875rem", color: "#374151" }}>
          <input
            type="checkbox"
            checked={tailOn}
            onChange={(e) => setTailOn(e.target.checked)}
            disabled={!selectedID}
          />
          Auto-refresh ({Math.round(TAIL_MS / 1000)}s)
        </label>
      </div>

      {(listError || error) != null && (
        <div style={{ marginBottom: "0.5rem" }}>
          <ErrorBanner error={error ?? listError} onRetry={retry} label="Error" />
        </div>
      )}

      <pre style={{
        background: "#111827",
        borderRadius: "0.5rem",
        padding: "1rem",
        fontSize: "0.75rem",
        lineHeight: "1.6",
        overflowX: "auto",
        overflowY: "auto",
        maxHeight: "60vh",
        margin: 0,
      }}>
        {logs && logs.lines.length > 0
          ? logs.lines.map((line, i) => (
              <span key={i} style={{ display: "block", ...lineStyle(line) }}>{line}</span>
            ))
          : <span style={{ color: "#6b7280" }}>{fetching ? "" : "No log lines"}</span>
        }
      </pre>
      <LastUpdated at={lastUpdated} stale={stale} intervalMs={tailOn ? TAIL_MS : undefined} />
    </div>
  );
}
