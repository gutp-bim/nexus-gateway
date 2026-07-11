// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import type { ConnectorItem, ConnectorLogs } from "@/lib/api";
import { apiFetch, isArrayOf, isRecord } from "@/lib/apiClient";
import { usePolling } from "@/lib/use-polling";
import { LastUpdated } from "@/components/last-updated";
import { ErrorBanner, messageFor } from "@/components/error-banner";

const TAIL_MS = 5_000;

// The gateway's own logs are a first-class source alongside each connector.
// The existing [id] proxy forwards id "gateway" to the backend /logs/gateway,
// so it needs no special client handling — just a selectable source id.
const GATEWAY_ID = "gateway";

type Severity = "error" | "warn" | "info" | "debug" | "unknown";
type SeverityFilter = "all" | "warn";

// Structured log lines are JSON. Gateway lines use slog keys (time/level/msg);
// connector lines use timestamp/level/message. Severity is read from the parsed
// `level` field (NOT a substring of the raw line), so a line whose message text
// merely contains the word "error" is not mistaken for an error.
type ParsedLine = { level: Severity; text: string };

function normalizeLevel(raw: unknown): Severity {
  if (typeof raw !== "string") return "unknown";
  const l = raw.trim().toLowerCase();
  if (l === "error" || l === "err" || l === "fatal" || l === "critical") return "error";
  if (l === "warn" || l === "warning") return "warn";
  if (l === "info" || l === "notice") return "info";
  if (l === "debug" || l === "trace") return "debug";
  return "unknown";
}

function parseLine(line: string): ParsedLine {
  try {
    const obj: unknown = JSON.parse(line);
    if (isRecord(obj)) {
      const level = normalizeLevel(obj.level);
      const msg = obj.msg ?? obj.message;
      return { level, text: typeof msg === "string" ? msg : line };
    }
  } catch {
    // Not JSON — fall through to raw line with unknown severity.
  }
  return { level: "unknown", text: line };
}

const SEVERITY_META: Record<Severity, { label: string; color: string }> = {
  error: { label: "ERROR", color: "#f87171" },
  warn: { label: "WARN", color: "#fbbf24" },
  info: { label: "INFO", color: "#93c5fd" },
  debug: { label: "DEBUG", color: "#9ca3af" },
  unknown: { label: "LOG", color: "#d1d5db" },
};

export default function LogsPage() {
  const [connectors, setConnectors] = useState<ConnectorItem[]>([]);
  // Default to the always-available Gateway source so there is a valid, labeled
  // selection immediately (before the connector list loads). Selecting a
  // connector still switches sources; the gateway is just the initial pick.
  const [selectedID, setSelectedID] = useState<string>(GATEWAY_ID);
  const [severity, setSeverity] = useState<SeverityFilter>("all");
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

  // Poll the selected source's log tail. The interval only runs while the tail
  // toggle is on (#41); selecting a source always fetches once via the effect
  // below, regardless of the toggle. Selection is preserved across refreshes.
  // Works identically for "gateway" and any connector id.
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

  // Fetch once whenever the selected source changes, independent of the tail toggle.
  useEffect(() => {
    if (selectedID) refresh();
  }, [selectedID, refresh]);

  // Retry both sources: a connector-list load failure must re-run that load (not
  // just the log poller), and a log failure re-polls the tail.
  const retry = useCallback(() => {
    loadConnectors();
    refresh();
  }, [loadConnectors, refresh]);

  const visibleLines = useMemo(() => {
    const parsed = (logs?.lines ?? []).map(parseLine);
    if (severity === "warn") {
      return parsed.filter((p) => p.level === "warn" || p.level === "error");
    }
    return parsed;
  }, [logs, severity]);

  return (
    <div>
      <div style={{ display: "flex", alignItems: "center", gap: "1rem", marginBottom: "1rem", flexWrap: "wrap" }}>
        <h1 style={{ fontSize: "1.25rem", fontWeight: 700, margin: 0 }}>Logs</h1>

        <label htmlFor="log-source" style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.875rem", color: "#374151" }}>
          Source
          <select
            id="log-source"
            aria-label="Log source"
            value={selectedID}
            onChange={(e) => setSelectedID(e.target.value)}
            style={{ padding: "0.3rem 0.6rem", borderRadius: "0.25rem", border: "1px solid #d1d5db", fontSize: "0.875rem" }}
          >
            {/* Gateway is always selectable, even with zero connectors. */}
            <option value={GATEWAY_ID}>Gateway</option>
            {connectors.map((c) => (
              <option key={c.id} value={c.id}>{c.id} {c.running ? "●" : "○"}</option>
            ))}
          </select>
        </label>

        <label htmlFor="log-severity" style={{ display: "flex", alignItems: "center", gap: "0.4rem", fontSize: "0.875rem", color: "#374151" }}>
          Severity
          <select
            id="log-severity"
            aria-label="Severity filter"
            value={severity}
            onChange={(e) => setSeverity(e.target.value as SeverityFilter)}
            style={{ padding: "0.3rem 0.6rem", borderRadius: "0.25rem", border: "1px solid #d1d5db", fontSize: "0.875rem" }}
          >
            <option value="all">All</option>
            <option value="warn">Warnings &amp; errors</option>
          </select>
        </label>

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
        {visibleLines.length > 0
          ? visibleLines.map((p, i) => {
              const meta = SEVERITY_META[p.level];
              return (
                <span key={i} style={{ display: "block", color: meta.color }}>
                  {/* Badge pairs the level text with color so severity is not
                      conveyed by color alone (accessibility). */}
                  <span
                    style={{
                      display: "inline-block",
                      minWidth: "3.5rem",
                      marginRight: "0.5rem",
                      fontWeight: 700,
                      color: meta.color,
                    }}
                  >
                    [{meta.label}]
                  </span>
                  {p.text}
                </span>
              );
            })
          : <span style={{ color: "#6b7280" }}>{fetching ? "" : "No log lines"}</span>
        }
      </pre>
      <LastUpdated at={lastUpdated} stale={stale} intervalMs={tailOn ? TAIL_MS : undefined} />
    </div>
  );
}
