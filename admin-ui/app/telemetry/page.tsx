// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback } from "react";
import { useTranslations } from "next-intl";
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
  const t = useTranslations("telemetry");
  const tc = useTranslations("common");
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

  if (loading && !data) return <p>{tc("loading")}</p>;

  const stats = data?.stats;
  const recent = data?.recent ?? [];

  // Prefer the backend's authoritative total; fall back to summing the per-point
  // map so an older gateway payload (no drift_total) still shows a real figure
  // consistent with the per-point table below.
  const driftEntries = stats
    ? Object.entries(stats.drifts ?? {}).sort(([, a], [, b]) => b - a)
    : [];
  const totalDrift = stats?.drift_total ?? driftEntries.reduce((sum, [, v]) => sum + v, 0);

  return (
    <div>
      <h1 style={{ fontSize: "1.25rem", fontWeight: 700, marginBottom: "1.25rem" }}>{t("title")}</h1>
      {error != null && (
        <div style={{ marginBottom: "0.75rem" }}>
          <ErrorBanner error={error} onRetry={refresh} label={t("loadError")} />
        </div>
      )}

      {/* Pipeline throughput + uplink health (#47). */}
      <div style={{ display: "flex", gap: "1rem", marginBottom: "1rem", flexWrap: "wrap" }}>
        <StatCard label={t("received")} value={fmtInt(stats?.received)} unit={t("unitFrames")} />
        <StatCard label={t("sent")} value={fmtInt(stats?.sent)} unit={t("unitFrames")} />
        <StatCard label={t("accepted")} value={fmtInt(stats?.accepted)} unit={t("unitFrames")} />
        <StatCard
          label={t("uplink")}
          value={
            stats == null
              ? "—"
              : stats.uplink_connected === undefined
                ? t("uplinkUnknown")
                : stats.uplink_connected
                  ? t("uplinkConnected")
                  : t("uplinkDisconnected")
          }
          alert={stats?.uplink_connected === false}
        />
        <StatCard label={t("lastCheckpoint")} value={fmtAgo(t, stats?.last_checkpoint_unix)} />
      </div>

      <div style={{ display: "flex", gap: "1rem", marginBottom: "1.5rem", flexWrap: "wrap" }}>
        <StatCard label={t("bufferDepth")} value={fmtInt(stats?.buffer_depth)} unit={t("unitFrames")} />
        <StatCard label={t("totalDrift")} value={fmtInt(totalDrift)} unit={t("unitFrames")} alert={totalDrift > 0} />
        <StatCard label={t("dropped")} value={fmtInt(stats?.dropped)} unit={t("unitFrames")} alert={(stats?.dropped ?? 0) > 0} />
        <StatCard label={t("sendErrors")} value={fmtInt(stats?.send_errors)} alert={(stats?.send_errors ?? 0) > 0} />
        <StatCard
          label={t("eventsStream")}
          value={stats?.events_stream ? fmtInt(stats.events_stream.msgs) : "—"}
          unit={stats?.events_stream ? `${t("unitMsgs")} · ${fmtBytes(stats.events_stream.bytes)}` : undefined}
        />
        <StatCard label={t("livePoints")} value={String(recent.length)} unit={t("unitPoints")} />
      </div>

      {recent.length > 0 && (
        <>
          <h2 style={{ fontSize: "1rem", fontWeight: 600, marginBottom: "0.5rem" }}>
            {t("latestValues")}{" "}
            <span style={{ fontWeight: 400, fontSize: "0.8rem", color: "#9ca3af" }}>
              {t("latestValuesNote", { seconds: Math.round(POLL_MS / 1000) })}
            </span>
          </h2>
          <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.875rem", marginBottom: "1.5rem" }}>
            <thead>
              <tr style={{ borderBottom: "2px solid #e5e7eb" }}>
                <th style={{ textAlign: "left", padding: "0.4rem 0.75rem" }}>{t("headerPointId")}</th>
                <th style={{ textAlign: "right", padding: "0.4rem 0.75rem" }}>{t("headerValue")}</th>
                <th style={{ textAlign: "left", padding: "0.4rem 0.75rem" }}>{t("headerTimestamp")}</th>
                <th style={{ textAlign: "left", padding: "0.4rem 0.75rem", color: "#9ca3af" }}>{t("headerReceivedAt")}</th>
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
        <p style={{ color: "#9ca3af", marginBottom: "1.5rem" }}>{t("noLiveValues")}</p>
      )}

      {driftEntries.length > 0 && (
        <>
          <h2 style={{ fontSize: "1rem", fontWeight: 600, marginBottom: "0.5rem" }}>{t("perPointDrift")}</h2>
          <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.875rem" }}>
            <thead>
              <tr style={{ borderBottom: "2px solid #e5e7eb" }}>
                <th style={{ textAlign: "left", padding: "0.4rem 0.75rem" }}>{t("headerPointId")}</th>
                <th style={{ textAlign: "right", padding: "0.4rem 0.75rem" }}>{t("headerDriftFrames")}</th>
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
        <p style={{ color: "#9ca3af" }}>{t("noDrift")}</p>
      )}
      <LastUpdated at={lastUpdated} stale={stale} intervalMs={POLL_MS} />
    </div>
  );
}

function fmtInt(n: number | undefined): string {
  return (n ?? 0).toLocaleString();
}

function fmtBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = bytes / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

/** Relative "N ago" for a unix-seconds checkpoint clock (0 / undefined = never). */
function fmtAgo(t: ReturnType<typeof useTranslations>, unix: number | undefined): string {
  if (!unix || unix <= 0) return t("never");
  const secs = Math.max(0, Math.floor(Date.now() / 1000 - unix));
  if (secs < 60) return t("secondsAgo", { n: secs });
  if (secs < 3600) return t("minutesAgo", { n: Math.floor(secs / 60) });
  if (secs < 86400) return t("hoursAgo", { n: Math.floor(secs / 3600) });
  return t("daysAgo", { n: Math.floor(secs / 86400) });
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
