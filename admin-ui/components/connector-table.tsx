// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback, useMemo, useState } from "react";
import {
  createColumnHelper,
  flexRender,
  getCoreRowModel,
  useReactTable,
} from "@tanstack/react-table";
import type { ConnectorItem } from "@/lib/api";
import { apiFetch, ApiError } from "@/lib/apiClient";

const col = createColumnHelper<ConnectorItem>();

type Props = {
  data: ConnectorItem[];
  isOperator: boolean;
  onRefresh: () => void;
};

export function ConnectorTable({ data, isOperator, onRefresh }: Props) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const doAction = useCallback(
    async (id: string, action: string, image?: string) => {
      setBusy(`${id}:${action}`);
      setError(null);
      try {
        const url = image
          ? `/api/gateway/connectors/${encodeURIComponent(id)}/${action}?image=${encodeURIComponent(image)}`
          : `/api/gateway/connectors/${encodeURIComponent(id)}/${action}`;
        await apiFetch(url, { method: "POST" });
        onRefresh();
      } catch (e) {
        setError(e instanceof ApiError ? e.message : String(e));
      } finally {
        setBusy(null);
      }
    },
    [onRefresh]
  );

  const columns = useMemo(() => [
    col.accessor("id", { header: "ID" }),
    col.accessor("image", {
      header: "Image / Digest",
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
      header: "Status",
      cell: (info) => (
        <span style={{ color: info.getValue() ? "#16a34a" : "#dc2626", fontWeight: 600 }}>
          {info.getValue() ? "Running" : "Stopped"}
        </span>
      ),
    }),
    col.display({
      id: "actions",
      header: "Actions",
      cell: (info) => {
        const { id, running, prev_image } = info.row.original;
        const isBusy = busy?.startsWith(`${id}:`);

        if (!isOperator) {
          return <span style={{ color: "#9ca3af", fontSize: "0.875rem" }}>viewer</span>;
        }
        return (
          <span style={{ display: "flex", gap: "0.4rem", flexWrap: "wrap" }}>
            {running ? (
              <>
                <ActionBtn label="Stop" disabled={!!isBusy} onClick={() => doAction(id, "stop")} />
                <ActionBtn label="Restart" disabled={!!isBusy} onClick={() => doAction(id, "restart")} />
              </>
            ) : (
              <ActionBtn label="Start" disabled={!!isBusy} onClick={() => doAction(id, "start")} />
            )}
            <ActionBtn
              label="Upgrade"
              disabled={!!isBusy}
              onClick={() => {
                const image = window.prompt("New image reference:", info.row.original.image);
                if (image) doAction(id, "upgrade", image);
              }}
            />
            {prev_image && (
              <ActionBtn
                label="Rollback"
                disabled={!!isBusy}
                onClick={() => {
                  if (window.confirm(`Roll back ${id} to:\n${prev_image}\n\nProceed?`)) {
                    doAction(id, "rollback");
                  }
                }}
                variant="danger"
              />
            )}
          </span>
        );
      },
    }),
  // eslint-disable-next-line react-hooks/exhaustive-deps
  ], [busy, isOperator, doAction]);

  const table = useReactTable({ data, columns, getCoreRowModel: getCoreRowModel() });

  return (
    <div>
      {error && <p style={{ color: "#dc2626", marginBottom: "0.5rem" }}>Error: {error}</p>}
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
                No connectors registered
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
    </div>
  );
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
