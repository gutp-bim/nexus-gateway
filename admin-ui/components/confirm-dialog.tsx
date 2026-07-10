// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Shared confirmation dialog (#40), replacing the native window.confirm on
// disruptive connector actions (Stop/Restart/Rollback). Unlike window.confirm it
// names the exact target and can carry extra context (e.g. the rollback image),
// so an operator can't fat-finger a stop on the wrong connector.

import { Dialog, DialogButton } from "@/components/dialog";

type Props = {
  open: boolean;
  title: string;
  /** Human-readable description of what will happen; the target is named here. */
  message: React.ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
};

export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel,
  cancelLabel = "Cancel",
  danger = false,
  onConfirm,
  onCancel,
}: Props) {
  return (
    <Dialog open={open} title={title} onClose={onCancel} titleId="confirm-dialog-title">
      <div style={{ fontSize: "0.9rem", color: "#374151", lineHeight: 1.5 }}>{message}</div>
      <div style={{ display: "flex", justifyContent: "flex-end", gap: "0.6rem", marginTop: "1.25rem" }}>
        <DialogButton label={cancelLabel} onClick={onCancel} variant="default" />
        <DialogButton label={confirmLabel} onClick={onConfirm} variant={danger ? "danger" : "primary"} autoFocus />
      </div>
    </Dialog>
  );
}
