// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback } from "react";
import type { PointEntry } from "@/lib/api";
import { apiFetch, isArrayOf } from "@/lib/apiClient";
import { usePolling } from "@/lib/use-polling";
import { LastUpdated } from "@/components/last-updated";
import { ErrorBanner } from "@/components/error-banner";

const POLL_MS = 30_000;

type Group = { connectorID: string; protocol: string; entries: PointEntry[] };

function groupByConnector(entries: PointEntry[]): Group[] {
  const map = new Map<string, Group>();
  for (const e of entries) {
    const key = e.connector_id;
    if (!map.has(key)) map.set(key, { connectorID: key, protocol: e.protocol, entries: [] });
    map.get(key)!.entries.push(e);
  }
  return [...map.values()];
}

export default function DevicesPage() {
  const fetchData = useCallback(
    () => apiFetch<PointEntry[]>("/api/gateway/devices", undefined, isArrayOf()),
    []
  );
  const { data: entries, error, loading, lastUpdated, stale, refresh } = usePolling(fetchData, {
    intervalMs: POLL_MS,
  });

  if (loading && !entries) return <p>Loading…</p>;

  const groups: Group[] = entries ? groupByConnector(entries) : [];

  return (
    <div>
      <h1 style={{ fontSize: "1.25rem", fontWeight: 700, marginBottom: "1.25rem" }}>Devices & Points</h1>
      {error != null && (
        <div style={{ marginBottom: "0.75rem" }}>
          <ErrorBanner error={error} onRetry={refresh} label="Failed to load" />
        </div>
      )}
      {entries && groups.length === 0 && !error && (
        <p style={{ color: "#9ca3af" }}>No points in Point List</p>
      )}
      {groups.map((g) => (
        <div key={g.connectorID} style={{ marginBottom: "1.5rem" }}>
          <h2 style={{ fontSize: "1rem", fontWeight: 600, marginBottom: "0.5rem" }}>
            {g.connectorID}
            <span style={{ marginLeft: "0.5rem", fontSize: "0.75rem", color: "#6b7280", fontWeight: 400 }}>
              {g.protocol}
            </span>
          </h2>
          <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.875rem" }}>
            <thead>
              <tr style={{ borderBottom: "2px solid #e5e7eb" }}>
                {["Point ID", "Local ID", "Device", "Unit", "Writable"].map((h) => (
                  <th key={h} style={{ textAlign: "left", padding: "0.4rem 0.75rem", whiteSpace: "nowrap", color: "#374151" }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {g.entries.map((e) => (
                <tr key={e.point_id} style={{ borderBottom: "1px solid #f3f4f6" }}>
                  <td style={{ padding: "0.4rem 0.75rem", fontFamily: "monospace", fontSize: "0.8rem" }}>{e.point_id}</td>
                  <td style={{ padding: "0.4rem 0.75rem", color: "#6b7280", fontSize: "0.8rem" }}>{e.local_id}</td>
                  <td style={{ padding: "0.4rem 0.75rem", color: "#6b7280" }}>{e.device_ref ?? "—"}</td>
                  <td style={{ padding: "0.4rem 0.75rem" }}>{e.unit ?? "—"}</td>
                  <td style={{ padding: "0.4rem 0.75rem" }}>
                    {e.writable ? (
                      <span style={{ color: "#2563eb", fontWeight: 600, fontSize: "0.75rem" }}>✓</span>
                    ) : (
                      <span style={{ color: "#d1d5db" }}>—</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ))}
      <LastUpdated at={lastUpdated} stale={stale} intervalMs={POLL_MS} />
    </div>
  );
}
