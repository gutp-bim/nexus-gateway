// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback } from "react";
import type { TelemetryStats } from "@/lib/api";
import { apiFetch, isRecord } from "@/lib/apiClient";
import { usePolling } from "@/lib/use-polling";
import { LastUpdated } from "@/components/last-updated";
import { ErrorBanner } from "@/components/error-banner";

const POLL_MS = 5_000;

type RecentEntry = {
  point_id: string;
  value: number;
  timestamp: string;
  received_at: string;
};

type TelemetryData = {
  stats: TelemetryStats;
  recent: RecentEntry[];
};

export default function TelemetryPage() {
  const fetchData = useCallback(async (): Promise<TelemetryData> => {
    const stats = await apiFetch<TelemetryStats>("/api/gateway/telemetry", undefined, isRecord);

    // Live values are ephemeral/best-effort (RecentStore has no persistence
    // guarantee), so a failure here must not mark the whole poll stale —
    // fall back to an empty table rather than losing the drift/buffer stats.
    let recent: RecentEntry[] = [];
    try {
      const recData = await apiFetch<{ values?: RecentEntry[] }>(
        "/api/gateway/recent",
        undefined,
        isRecord
      );
      recent = (recData.values ?? [])
        .slice()
        .sort((a, b) => a.point_id.localeCompare(b.point_id));
    } catch {
      // best-effort — keep stats even if /recent is unavailable
    }

    return { stats, recent };
  }, []);

  const { data, error, loading, lastUpdated, stale, refresh } = usePolling(fetchData, {
    intervalMs: POLL_MS,
  });

  if (loading && !data) return <p>Loading…</p>;

  const stats = data?.stats;
  const recent = data?.recent ?? [];

  const totalDrift = stats ? Object.values(stats.drifts).reduce((a, b) => a + b, 0) : 0;
  const driftEntries = stats
    ? Object.entries(stats.drifts).sort(([, a], [, b]) => b - a)
    : [];

  return (
    <div>
      <h1 style={{ fontSize: "1.25rem", fontWeight: 700, marginBottom: "1.25rem" }}>Telemetry Monitor</h1>
      {error != null && (
        <div style={{ marginBottom: "0.75rem" }}>
          <ErrorBanner error={error} onRetry={refresh} label="Failed to load" />
        </div>
      )}

      <div style={{ display: "flex", gap: "1rem", marginBottom: "1.5rem", flexWrap: "wrap" }}>
        <StatCard label="S&F Buffer Depth" value={String(stats?.buffer_depth ?? 0)} unit="frames" />
        <StatCard label="Total Drift" value={String(totalDrift)} unit="frames" alert={totalDrift > 0} />
        <StatCard label="Points w/ Drift" value={String(driftEntries.filter(([, v]) => v > 0).length)} unit={`/ ${driftEntries.length}`} />
        <StatCard label="Live Points" value={String(recent.length)} unit="points" />
      </div>

      {recent.length > 0 && (
        <>
          <h2 style={{ fontSize: "1rem", fontWeight: 600, marginBottom: "0.5rem" }}>
            Latest Values{" "}
            <span style={{ fontWeight: 400, fontSize: "0.8rem", color: "#9ca3af" }}>
              (refreshes every 5 s - ephemeral, lost on restart)
            </span>
          </h2>
          <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.875rem", marginBottom: "1.5rem" }}>
            <thead>
              <tr style={{ borderBottom: "2px solid #e5e7eb" }}>
                <th style={{ textAlign: "left", padding: "0.4rem 0.75rem" }}>Point ID</th>
                <th style={{ textAlign: "right", padding: "0.4rem 0.75rem" }}>Value</th>
                <th style={{ textAlign: "left", padding: "0.4rem 0.75rem" }}>Timestamp</th>
                <th style={{ textAlign: "left", padding: "0.4rem 0.75rem", color: "#9ca3af" }}>Received At</th>
              </tr>
            </thead>
            <tbody>
              {recent.map((e) => (
                <tr key={e.point_id} style={{ borderBottom: "1px solid #f3f4f6" }}>
                  <td style={{ padding: "0.4rem 0.75rem", fontFamily: "monospace", fontSize: "0.8rem", fontWeight: 600 }}>{e.point_id}</td>
                  <td style={{ padding: "0.4rem 0.75rem", textAlign: "right" }}>{e.value.toFixed(4)}</td>
                  <td style={{ padding: "0.4rem 0.75rem", fontSize: "0.78rem", color: "#374151" }}>{e.timestamp}</td>
                  <td style={{ padding: "0.4rem 0.75rem", fontSize: "0.78rem", color: "#9ca3af" }}>{e.received_at}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
      {recent.length === 0 && !error && (
        <p style={{ color: "#9ca3af", marginBottom: "1.5rem" }}>No live values yet - waiting for telemetry events...</p>
      )}

      {driftEntries.length > 0 && (
        <>
          <h2 style={{ fontSize: "1rem", fontWeight: 600, marginBottom: "0.5rem" }}>Per-Point Drift</h2>
          <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.875rem" }}>
            <thead>
              <tr style={{ borderBottom: "2px solid #e5e7eb" }}>
                <th style={{ textAlign: "left", padding: "0.4rem 0.75rem" }}>Point ID</th>
                <th style={{ textAlign: "right", padding: "0.4rem 0.75rem" }}>Drift (frames)</th>
              </tr>
            </thead>
            <tbody>
              {driftEntries.map(([pid, drift]) => (
                <tr key={pid} style={{ borderBottom: "1px solid #f3f4f6" }}>
                  <td style={{ padding: "0.4rem 0.75rem", fontFamily: "monospace", fontSize: "0.8rem" }}>{pid}</td>
                  <td style={{
                    padding: "0.4rem 0.75rem",
                    textAlign: "right",
                    fontWeight: drift > 0 ? 600 : 400,
                    color: drift > 0 ? "#dc2626" : "#6b7280",
                  }}>
                    {drift}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
      {driftEntries.length === 0 && recent.length > 0 && (
        <p style={{ color: "#9ca3af" }}>No drift - all points accepted by Building OS</p>
      )}
      <LastUpdated at={lastUpdated} stale={stale} intervalMs={POLL_MS} />
    </div>
  );
}

function StatCard({ label, value, unit, alert }: { label: string; value: string; unit?: string; alert?: boolean }) {
  return (
    <div style={{
      border: `1px solid ${alert ? "#fca5a5" : "#e5e7eb"}`,
      borderRadius: "0.5rem",
      padding: "0.75rem 1.25rem",
      background: alert ? "#fef2f2" : "#fff",
      minWidth: "10rem",
    }}>
      <div style={{ fontSize: "0.75rem", color: "#6b7280", marginBottom: "0.25rem" }}>{label}</div>
      <div style={{ fontSize: "1.5rem", fontWeight: 700, color: alert ? "#dc2626" : "#111827" }}>
        {value}
        {unit && <span style={{ fontSize: "0.875rem", fontWeight: 400, color: "#6b7280", marginLeft: "0.3rem" }}>{unit}</span>}
      </div>
    </div>
  );
}
