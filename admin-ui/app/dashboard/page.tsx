// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useEffect, useState } from "react";
import type { GatewayHealth } from "@/lib/api";

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
  const [health, setHealth] = useState<GatewayHealth | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  const fetchHealth = async () => {
    try {
      const res = await fetch("/api/gateway/health");
      if (!res.ok) throw new Error(`${res.status}`);
      const data: GatewayHealth = await res.json();
      setHealth(data);
      setLastUpdated(new Date());
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  };

  useEffect(() => {
    fetchHealth();
    const id = setInterval(fetchHealth, 5_000);
    return () => clearInterval(id);
  }, []);

  if (error) return <p style={{ color: "#dc2626" }}>Failed to load health: {error}</p>;
  if (!health) return <p>Loading…</p>;

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
      <h1 style={{ fontSize: "1.25rem", fontWeight: 700, marginBottom: "1.25rem" }}>Gateway Dashboard</h1>
      <div style={{ display: "flex", gap: "1rem", flexWrap: "wrap", marginBottom: "1.5rem" }}>
        <StatCard label="Status" value={running === total && total > 0 ? "✓ OK" : total === 0 ? "No connectors" : `${running}/${total} running`} />
        <StatCard label="Uptime" value={uptimeStr} />
        <StatCard label="Memory" value={`${fmt(health.MemAllocMB)} MB`} />
        <StatCard label="Goroutines" value={String(health.GoRoutines)} />
        <StatCard
          label="Disk"
          value={health.DiskTotalMB > 0 ? `${fmt(health.DiskUsedMB / 1024)} / ${fmt(health.DiskTotalMB / 1024)} GB (${diskPct}%)` : "—"}
        />
      </div>
      {lastUpdated && (
        <p style={{ fontSize: "0.75rem", color: "#9ca3af" }}>
          Last updated: {lastUpdated.toLocaleTimeString()} — refreshing every 5 s
        </p>
      )}
    </div>
  );
}
