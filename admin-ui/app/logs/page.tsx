// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ConnectorItem, ConnectorLogs } from "@/lib/api";

export default function LogsPage() {
  const [connectors, setConnectors] = useState<ConnectorItem[]>([]);
  const [selectedID, setSelectedID] = useState<string>("");
  const [logs, setLogs] = useState<ConnectorLogs | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const fetchingRef = useRef(false);

  // Load connector list once on mount
  useEffect(() => {
    fetch("/api/gateway/connectors")
      .then((r) => r.json())
      .then((items: ConnectorItem[]) => {
        setConnectors(items);
        if (items.length > 0) setSelectedID(items[0].id);
      })
      .catch((e) => setError(String(e)));
  }, []);

  const fetchLogs = useCallback(async (id: string) => {
    if (!id || fetchingRef.current) return;
    fetchingRef.current = true;
    setLoading(true);
    setError(null);
    try {
      const res = await fetch(`/api/gateway/logs/${encodeURIComponent(id)}?tail=200`);
      if (!res.ok) throw new Error(`logs: ${res.status}`);
      setLogs(await res.json());
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
      fetchingRef.current = false;
    }
  }, []);

  useEffect(() => {
    if (selectedID) fetchLogs(selectedID);
  }, [selectedID, fetchLogs]);

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
          onClick={() => fetchLogs(selectedID)}
          disabled={loading || !selectedID}
          style={{
            padding: "0.3rem 0.75rem",
            fontSize: "0.875rem",
            border: "1px solid #d1d5db",
            borderRadius: "0.25rem",
            cursor: loading ? "not-allowed" : "pointer",
            opacity: loading ? 0.5 : 1,
          }}
        >
          {loading ? "Loading…" : "Reload"}
        </button>
      </div>

      {error && <p style={{ color: "#dc2626", marginBottom: "0.5rem" }}>Error: {error}</p>}

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
          : <span style={{ color: "#6b7280" }}>{loading ? "" : "No log lines"}</span>
        }
      </pre>
    </div>
  );
}
