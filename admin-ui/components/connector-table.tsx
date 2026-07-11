// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  createColumnHelper,
  flexRender,
  getCoreRowModel,
  useReactTable,
} from "@tanstack/react-table";
import { useTranslations } from "next-intl";
import type { CatalogEntry, Capabilities, ConnectorItem } from "@/lib/api";
import { apiFetch, isArrayOf, isRecord } from "@/lib/apiClient";
import { useToast } from "@/components/toast";
import { messageFor } from "@/components/error-banner";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { UpgradeDialog } from "@/components/upgrade-dialog";

const col = createColumnHelper<ConnectorItem>();

type Props = {
  data: ConnectorItem[];
  isOperator: boolean;
  onRefresh: () => void;
};

// A disruptive action awaiting confirmation.
type PendingConfirm = {
  id: string;
  action: string;
  title: string;
  message: React.ReactNode;
  confirmLabel: string;
  danger: boolean;
};

export function ConnectorTable({ data, isOperator, onRefresh }: Props) {
  const toast = useToast();
  const t = useTranslations("connectorTable");
  const tc = useTranslations("common");
  const [busy, setBusy] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<PendingConfirm | null>(null);
  const [upgradeFor, setUpgradeFor] = useState<ConnectorItem | null>(null);

  // Supporting data for the Upgrade dialog. Fetched once; failures degrade
  // safely (no catalog target shown, ad-hoc stays hidden) rather than blocking
  // the whole table, since these are secondary to the connector list itself.
  const [catalog, setCatalog] = useState<CatalogEntry[]>([]);
  const [allowAdhoc, setAllowAdhoc] = useState(false);

  useEffect(() => {
    if (!isOperator) return; // viewers can't act, so skip the extra fetches
    let cancelled = false;
    (async () => {
      try {
        const entries = await apiFetch<CatalogEntry[]>("/api/gateway/catalog", undefined, isArrayOf());
        if (!cancelled) setCatalog(entries);
      } catch {
        /* catalog is optional context; leave empty on failure */
      }
      try {
        const caps = await apiFetch<Capabilities>("/api/gateway/capabilities", undefined, isRecord);
        if (!cancelled) setAllowAdhoc(caps.allow_adhoc_upgrade === true);
      } catch {
        /* default-safe: ad-hoc stays disabled */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [isOperator]);

  const doAction = useCallback(
    async (id: string, action: string, image?: string) => {
      setBusy(`${id}:${action}`);
      try {
        const url = image
          ? `/api/gateway/connectors/${encodeURIComponent(id)}/${action}?image=${encodeURIComponent(image)}`
          : `/api/gateway/connectors/${encodeURIComponent(id)}/${action}`;
        await apiFetch(url, { method: "POST" });
        toast.success(t("toastSuccess", { action: labelForAction(t, action), id }));
        onRefresh();
      } catch (e) {
        toast.error(t("toastFailure", { action: labelForAction(t, action), id, error: messageFor(e) }));
      } finally {
        setBusy(null);
      }
    },
    [onRefresh, toast, t]
  );

  const columns = useMemo(() => [
    col.accessor("id", { header: t("headerId") }),
    col.accessor("image", {
      header: t("headerImage"),
      cell: (info) => {
        const img = info.getValue();
        if (!img) return "—";
        const atIdx = img.indexOf("@");
        const digest = atIdx >= 0 ? img.slice(atIdx + 1) : null;
        const base = atIdx >= 0 ? img.slice(0, atIdx) : img;
        const short = base.slice(base.lastIndexOf("/") + 1);
        return (
          <span title={img}>
            <span>{short}</span>
            {digest && (
              <span style={{ marginLeft: "0.4rem", fontFamily: "monospace", fontSize: "0.75em", color: "#6b7280" }}>
                {shortDigest(digest)}
              </span>
            )}
          </span>
        );
      },
    }),
    col.accessor("running", {
      header: t("headerStatus"),
      cell: (info) => (
        <span style={{ color: info.getValue() ? "#16a34a" : "#dc2626", fontWeight: 600 }}>
          {info.getValue() ? t("running") : t("stopped")}
        </span>
      ),
    }),
    col.display({
      id: "actions",
      header: t("headerActions"),
      cell: (info) => {
        const conn = info.row.original;
        const { id, running, prev_image } = conn;
        const isBusy = busy?.startsWith(`${id}:`);

        if (!isOperator) {
          return <span style={{ color: "#9ca3af", fontSize: "0.875rem" }}>{t("viewer")}</span>;
        }
        return (
          <span style={{ display: "flex", gap: "0.4rem", flexWrap: "wrap" }}>
            {running ? (
              <>
                <ActionBtn
                  label={t("stop")}
                  disabled={!!isBusy}
                  onClick={() =>
                    setConfirm({
                      id,
                      action: "stop",
                      title: t("confirmStopTitle", { id }),
                      message: t.rich("confirmStopMessage", { id, strong: (c) => <strong>{c}</strong> }),
                      confirmLabel: t("stop"),
                      danger: true,
                    })
                  }
                  variant="danger"
                />
                <ActionBtn
                  label={t("restart")}
                  disabled={!!isBusy}
                  onClick={() =>
                    setConfirm({
                      id,
                      action: "restart",
                      title: t("confirmRestartTitle", { id }),
                      message: t.rich("confirmRestartMessage", { id, strong: (c) => <strong>{c}</strong> }),
                      confirmLabel: t("restart"),
                      danger: false,
                    })
                  }
                />
              </>
            ) : (
              <ActionBtn label={t("start")} disabled={!!isBusy} onClick={() => doAction(id, "start")} />
            )}
            <ActionBtn label={t("upgrade")} disabled={!!isBusy} onClick={() => setUpgradeFor(conn)} />
            {prev_image && (
              <ActionBtn
                label={t("rollback")}
                disabled={!!isBusy}
                onClick={() =>
                  setConfirm({
                    id,
                    action: "rollback",
                    title: t("confirmRollbackTitle", { id }),
                    message: (
                      <>
                        {t.rich("confirmRollbackMessage", { id, strong: (c) => <strong>{c}</strong> })}
                        <br />
                        <span style={{ fontFamily: "monospace", fontSize: "0.8rem", color: "#6b7280", wordBreak: "break-all" }}>
                          {prev_image}
                        </span>
                      </>
                    ),
                    confirmLabel: t("confirmRollbackLabel"),
                    danger: true,
                  })
                }
                variant="danger"
              />
            )}
          </span>
        );
      },
    }),
  // eslint-disable-next-line react-hooks/exhaustive-deps
  ], [busy, isOperator, doAction, t]);

  const table = useReactTable({ data, columns, getCoreRowModel: getCoreRowModel() });

  return (
    <div>
      <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "0.9rem" }}>
        <thead>
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id} style={{ borderBottom: "2px solid #e5e7eb" }}>
              {hg.headers.map((h) => (
                <th key={h.id} style={{ textAlign: "left", padding: "0.5rem 0.75rem", whiteSpace: "nowrap" }}>
                  {flexRender(h.column.columnDef.header, h.getContext())}
                </th>
              ))}
            </tr>
          ))}
        </thead>
        <tbody>
          {table.getRowModel().rows.length === 0 ? (
            <tr>
              <td colSpan={columns.length} style={{ padding: "1rem", color: "#9ca3af", textAlign: "center" }}>
                {t("empty")}
              </td>
            </tr>
          ) : (
            table.getRowModel().rows.map((row) => (
              <tr key={row.id} style={{ borderBottom: "1px solid #f3f4f6" }}>
                {row.getVisibleCells().map((cell) => (
                  <td key={cell.id} style={{ padding: "0.5rem 0.75rem" }}>
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>

      <ConfirmDialog
        open={confirm !== null}
        title={confirm?.title ?? ""}
        message={confirm?.message}
        confirmLabel={confirm?.confirmLabel ?? tc("confirm")}
        cancelLabel={tc("cancel")}
        danger={confirm?.danger ?? false}
        onConfirm={() => {
          if (confirm) doAction(confirm.id, confirm.action);
          setConfirm(null);
        }}
        onCancel={() => setConfirm(null)}
      />

      {upgradeFor && (
        <UpgradeDialog
          key={upgradeFor.id}
          open
          connectorId={upgradeFor.id}
          currentImage={upgradeFor.image}
          catalogEntry={catalog.find((e) => e.name === upgradeFor.id)}
          allowAdhoc={allowAdhoc}
          onUpdate={() => {
            doAction(upgradeFor.id, "update");
            setUpgradeFor(null);
          }}
          onUpgrade={(image) => {
            doAction(upgradeFor.id, "upgrade", image);
            setUpgradeFor(null);
          }}
          onCancel={() => setUpgradeFor(null)}
        />
      )}
    </div>
  );
}

// Localized action verb for toast messages. `t` is the "connectorTable"
// namespace translator; unknown actions fall back to the raw action string.
function labelForAction(t: ReturnType<typeof useTranslations>, action: string): string {
  switch (action) {
    case "start":
      return t("start");
    case "stop":
      return t("stop");
    case "restart":
      return t("restart");
    case "upgrade":
      return t("upgrade");
    case "update":
      return t("update");
    case "rollback":
      return t("rollback");
    default:
      return action;
  }
}

function shortDigest(d: string): string {
  if (!d) return "—";
  const hex = d.startsWith("sha256:") ? d.slice(7) : d.includes(":") ? d.slice(d.indexOf(":") + 1) : d;
  return hex.length >= 12 ? `${hex.slice(0, 12)}…` : hex || "—";
}

type Variant = "default" | "danger";

function ActionBtn({
  label, disabled, onClick, variant = "default",
}: {
  label: string; disabled: boolean; onClick: () => void; variant?: Variant;
}) {
  const borderColor = variant === "danger" ? "#dc2626" : "#d1d5db";
  const color = variant === "danger" ? "#dc2626" : undefined;
  return (
    <button
      disabled={disabled}
      onClick={onClick}
      style={{
        padding: "0.2rem 0.6rem",
        fontSize: "0.8rem",
        cursor: disabled ? "not-allowed" : "pointer",
        opacity: disabled ? 0.5 : 1,
        border: `1px solid ${borderColor}`,
        borderRadius: "0.25rem",
        background: "#fff",
        color,
      }}
    >
      {label}
    </button>
  );
}
