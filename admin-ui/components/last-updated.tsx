// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Shared last-updated / stale indicator (#41). Generalizes the dashboard's
// inline "Last updated …" line so every polling screen shows freshness and
// flags stale data (last poll failed) — with a text marker, not color alone.

export function LastUpdated({
  at,
  stale,
  intervalMs,
}: {
  at: Date | null;
  stale?: boolean;
  intervalMs?: number;
}) {
  if (!at) return null;
  const refreshHint = intervalMs ? ` — refreshing every ${Math.round(intervalMs / 1000)} s` : "";
  return (
    <p style={{ fontSize: "0.75rem", color: stale ? "#b45309" : "#9ca3af", margin: "0.25rem 0" }}>
      {stale && <span style={{ fontWeight: 600 }}>⚠ Stale — </span>}
      Last updated: {at.toLocaleTimeString()}
      {refreshHint}
    </p>
  );
}
