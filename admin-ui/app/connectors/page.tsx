// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback } from "react";
import { useSession } from "next-auth/react";
import { useTranslations } from "next-intl";
import { ConnectorTable } from "@/components/connector-table";
import type { ConnectorItem } from "@/lib/api";
import { apiFetch, isArrayOf } from "@/lib/apiClient";
import { usePolling } from "@/lib/use-polling";
import { LastUpdated } from "@/components/last-updated";
import { ErrorBanner } from "@/components/error-banner";

const POLL_MS = 10_000;

export default function ConnectorsPage() {
  const { data: session } = useSession();
  const t = useTranslations("connectors");
  const tc = useTranslations("common");
  const fetchConnectors = useCallback(
    () => apiFetch<ConnectorItem[]>("/api/gateway/connectors", undefined, isArrayOf()),
    []
  );
  const { data: connectors, error, loading, lastUpdated, stale, refresh } = usePolling(fetchConnectors, {
    intervalMs: POLL_MS,
  });

  const isOperator = session?.realmRoles?.includes("gateway-operator") ?? false;

  return (
    <div>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: "1.25rem" }}>
        <h1 style={{ fontSize: "1.25rem", fontWeight: 700, margin: 0 }}>{t("title")}</h1>
        {!isOperator && (
          <span style={{ fontSize: "0.8rem", color: "#6b7280", background: "#f3f4f6", padding: "0.2rem 0.6rem", borderRadius: "999px" }}>
            {t("viewerBadge")}
          </span>
        )}
      </div>
      {loading && !connectors && <p>{tc("loading")}</p>}
      {error != null && (
        <div style={{ marginBottom: "0.75rem" }}>
          <ErrorBanner error={error} onRetry={refresh} label={t("loadError")} />
        </div>
      )}
      {connectors && (
        <>
          <ConnectorTable data={connectors} isOperator={isOperator} onRefresh={refresh} />
          <LastUpdated at={lastUpdated} stale={stale} intervalMs={POLL_MS} />
        </>
      )}
    </div>
  );
}
