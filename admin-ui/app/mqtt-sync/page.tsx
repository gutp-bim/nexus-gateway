// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback, useMemo, useState } from "react";
import { useSession } from "next-auth/react";
import { useTranslations } from "next-intl";
import type { MqttSyncDiff, PointEntry, SubscriptionSpec } from "@/lib/api";
import { apiFetch, isArrayOf } from "@/lib/apiClient";
import { usePolling } from "@/lib/use-polling";
import { LastUpdated } from "@/components/last-updated";
import { ErrorBanner, messageFor } from "@/components/error-banner";
import { useToast } from "@/components/toast";

const POLL_MS = 15_000;

function shortRevision(rev: string): string {
  if (!rev) return "—";
  const hex = rev.startsWith("sha256:") ? rev.slice(7) : rev;
  return hex.length > 12 ? `${hex.slice(0, 12)}…` : hex;
}

export default function MqttSyncPage() {
  const { data: session } = useSession();
  const toast = useToast();
  const t = useTranslations("mqttSync");
  const tc = useTranslations("common");
  const [connectorID, setConnectorID] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);

  const fetchDevices = useCallback(
    () => apiFetch<PointEntry[]>("/api/gateway/devices", undefined, isArrayOf()),
    []
  );
  const devices = usePolling(fetchDevices, { intervalMs: POLL_MS });

  const mqttConnectorIDs = useMemo(() => {
    const ids = new Set<string>();
    for (const e of devices.data ?? []) {
      if (e.protocol === "mqtt") ids.add(e.connector_id);
    }
    return [...ids].sort();
  }, [devices.data]);

  const selected = connectorID ?? mqttConnectorIDs[0] ?? null;

  const fetchDiff = useCallback(async (): Promise<MqttSyncDiff | null> => {
    if (!selected) return null;
    return apiFetch<MqttSyncDiff>(`/api/gateway/connectors/${encodeURIComponent(selected)}/mqtt-subscriptions/preview`);
  }, [selected]);

  const { data: diff, error, loading, lastUpdated, stale, refresh } = usePolling(fetchDiff, {
    intervalMs: POLL_MS,
  });

  const isOperator = session?.realmRoles?.includes("gateway-operator") ?? false;
  const hasChanges = !!diff && ((diff.added?.length ?? 0) + (diff.changed?.length ?? 0) + (diff.removed?.length ?? 0)) > 0;

  const doApply = async () => {
    if (!selected) return;
    setApplying(true);
    try {
      const reply = await apiFetch(`/api/gateway/connectors/${encodeURIComponent(selected)}/mqtt-subscriptions/apply`, { method: "POST" });
      const r = reply as { applied: boolean; errors?: string[] };
      if (r.applied) {
        toast.success(t("toastApplied", { id: selected }));
      } else {
        toast.error(t("toastApplyFailed", { id: selected, error: (r.errors ?? []).join(", ") }));
      }
      await refresh();
    } catch (e) {
      toast.error(t("toastApplyFailed", { id: selected, error: messageFor(e) }));
    } finally {
      setApplying(false);
    }
  };

  if (devices.loading && !devices.data) return <p>{tc("loading")}</p>;

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

      {mqttConnectorIDs.length === 0 ? (
        <p style={{ color: "#9ca3af" }}>{t("noConnectors")}</p>
      ) : (
        <>
          <div style={{ marginBottom: "1rem", display: "flex", alignItems: "center", gap: "0.5rem" }}>
            <label htmlFor="mqtt-sync-connector" style={{ fontSize: "0.875rem", color: "#374151" }}>
              {t("connectorLabel")}
            </label>
            <select
              id="mqtt-sync-connector"
              value={selected ?? ""}
              onChange={(e) => setConnectorID(e.target.value)}
              style={{ padding: "0.25rem 0.5rem", fontSize: "0.875rem" }}
            >
              {mqttConnectorIDs.map((id) => (
                <option key={id} value={id}>{id}</option>
              ))}
            </select>
          </div>

          {error != null && (
            <div style={{ marginBottom: "0.75rem" }}>
              <ErrorBanner error={error} onRetry={refresh} label={t("loadError")} />
            </div>
          )}

          {diff && (
            <>
              <div style={{ display: "flex", gap: "1.5rem", marginBottom: "1rem", fontSize: "0.875rem", color: "#374151" }}>
                <span>{t("subscribedCount", { count: diff.subscribed_count })}</span>
                <span title={diff.current_revision}>{t("currentRevision", { rev: shortRevision(diff.current_revision) })}</span>
                <span title={diff.target_revision}>{t("targetRevision", { rev: shortRevision(diff.target_revision) })}</span>
              </div>

              {!hasChanges ? (
                <p style={{ color: "#16a34a", marginBottom: "1rem" }}>{t("inSync")}</p>
              ) : (
                <>
                  <DiffTable title={t("added")} specs={diff.added} tone="#16a34a" />
                  <DiffTable title={t("changed")} specs={diff.changed} tone="#d97706" />
                  <DiffTable title={t("removed")} specs={diff.removed} tone="#dc2626" />
                </>
              )}

              {isOperator && (
                <button
                  disabled={!hasChanges || applying || loading}
                  onClick={doApply}
                  style={{
                    marginTop: "0.5rem",
                    padding: "0.4rem 1rem",
                    fontSize: "0.9rem",
                    cursor: !hasChanges || applying || loading ? "not-allowed" : "pointer",
                    opacity: !hasChanges || applying || loading ? 0.5 : 1,
                    border: "1px solid #2563eb",
                    borderRadius: "0.25rem",
                    background: "#2563eb",
                    color: "#fff",
                  }}
                >
                  {applying ? t("applying") : t("apply")}
                </button>
              )}
            </>
          )}
          <LastUpdated at={lastUpdated} stale={stale} intervalMs={POLL_MS} />
        </>
      )}
    </div>
  );
}

function DiffTable({ title, specs, tone }: { title: string; specs?: SubscriptionSpec[]; tone: string }) {
  const tc = useTranslations("common");
  if (!specs || specs.length === 0) return null;
  return (
    <div style={{ marginBottom: "1rem" }}>
      <h2 style={{ fontSize: "0.9rem", fontWeight: 600, color: tone, marginBottom: "0.35rem" }}>
        {title} ({specs.length})
      </h2>
      <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.8rem" }}>
        <tbody>
          {specs.map((s) => (
            <tr key={s.topic} style={{ borderBottom: "1px solid #f3f4f6" }}>
              <td style={{ padding: "0.3rem 0.6rem", fontFamily: "monospace" }}>{s.topic}</td>
              <td style={{ padding: "0.3rem 0.6rem", color: "#6b7280" }}>QoS {s.qos}</td>
              <td style={{ padding: "0.3rem 0.6rem", color: "#6b7280" }}>{s.device_ref ?? tc("dash")}</td>
              <td style={{ padding: "0.3rem 0.6rem", color: "#6b7280" }}>
                {s.writable ? `writable (cmd QoS ${s.command_qos ?? 1})` : tc("dash")}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
