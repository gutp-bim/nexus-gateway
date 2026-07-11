// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback } from "react";
import { useTranslations } from "next-intl";
import type { GatewayHealth } from "@/lib/api";
import { apiFetch, isRecord } from "@/lib/apiClient";
import { usePolling } from "@/lib/use-polling";
import { LastUpdated } from "@/components/last-updated";
import { ErrorBanner } from "@/components/error-banner";

const POLL_MS = 5_000;

function fmt(n: number, decimals = 1) {
  return n.toFixed(decimals);
}

function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <div
      style={{
        background: "#fff",
        border: "1px solid #e5e7eb",
        borderRadius: "0.5rem",
        padding: "1rem 1.5rem",
        minWidth: "160px",
      }}
    >
      <p style={{ margin: 0, fontSize: "0.75rem", color: "#6b7280", textTransform: "uppercase", letterSpacing: "0.05em" }}>
        {label}
      </p>
      <p style={{ margin: "0.25rem 0 0", fontSize: "1.5rem", fontWeight: 700 }}>{value}</p>
    </div>
  );
}

export default function DashboardPage() {
  const t = useTranslations("dashboard");
  const tc = useTranslations("common");
  const fetchHealth = useCallback(
    () => apiFetch<GatewayHealth>("/api/gateway/health", undefined, isRecord),
    []
  );
  const { data: health, error, loading, lastUpdated, stale, refresh } = usePolling(fetchHealth, {
    intervalMs: POLL_MS,
  });

  // Only blank the screen before the very first result. After that a failed
  // poll keeps the last-known health with a stale badge instead of a wipe.
  if (loading && !health) return <p>{tc("loading")}</p>;
  if (error && !health) return <ErrorBanner error={error} onRetry={refresh} label={t("loadError")} />;
  if (!health) return <p>{tc("loading")}</p>;

  const uptimeSec = health.UptimeSeconds;
  const h = Math.floor(uptimeSec / 3600);
  const m = Math.floor((uptimeSec % 3600) / 60);
  const s = Math.floor(uptimeSec % 60);
  const uptimeStr = `${h}h ${m}m ${s}s`;

  const diskPct = health.DiskTotalMB > 0
    ? ((health.DiskUsedMB / health.DiskTotalMB) * 100).toFixed(1)
    : "—";

  const running = (health.Connectors ?? []).filter((c) => c.Running).length;
  const total = (health.Connectors ?? []).length;

  return (
    <div>
      <h1 style={{ fontSize: "1.25rem", fontWeight: 700, marginBottom: "1.25rem" }}>{t("title")}</h1>
      <div style={{ display: "flex", gap: "1rem", flexWrap: "wrap", marginBottom: "1.5rem" }}>
        <StatCard
          label={t("statusLabel")}
          value={
            running === total && total > 0
              ? t("statusOk")
              : total === 0
                ? t("statusNoConnectors")
                : t("statusRunning", { running, total })
          }
        />
        <StatCard label={t("uptime")} value={uptimeStr} />
        <StatCard label={t("memory")} value={`${fmt(health.MemAllocMB)} MB`} />
        <StatCard label={t("cpu")} value={`${fmt(health.CPUPercent ?? 0)}%`} />
        <StatCard label={t("goroutines")} value={String(health.GoRoutines)} />
        <StatCard
          label={t("disk")}
          value={health.DiskTotalMB > 0 ? `${fmt(health.DiskUsedMB / 1024)} / ${fmt(health.DiskTotalMB / 1024)} GB (${diskPct}%)` : "—"}
        />
      </div>
      {stale && (
        <div style={{ marginBottom: "0.75rem" }}>
          <ErrorBanner error={error} onRetry={refresh} label={t("refreshFailed")} />
        </div>
      )}
      <LastUpdated at={lastUpdated} stale={stale} intervalMs={POLL_MS} />
    </div>
  );
}
