// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Shared last-updated / stale indicator (#41). Generalizes the dashboard's
// inline "Last updated …" line so every polling screen shows freshness and
// flags stale data (last poll failed) — with a text marker, not color alone.

import { useTranslations } from "next-intl";

export function LastUpdated({
  at,
  stale,
  intervalMs,
}: {
  at: Date | null;
  stale?: boolean;
  intervalMs?: number;
}) {
  const t = useTranslations("lastUpdated");
  if (!at) return null;
  const refreshHint = intervalMs ? t("refreshHint", { seconds: Math.round(intervalMs / 1000) }) : "";
  return (
    <p style={{ fontSize: "0.75rem", color: stale ? "#b45309" : "#9ca3af", margin: "0.25rem 0" }}>
      {stale && <span style={{ fontWeight: 600 }}>{t("stale")}</span>}
      {t("label", { time: at.toLocaleTimeString() })}
      {refreshHint}
    </p>
  );
}
