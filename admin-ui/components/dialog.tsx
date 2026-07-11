// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Minimal accessible modal (#40). The admin-ui had no modal primitive, so
// destructive actions leaned on native window.confirm/prompt — unstyleable and
// impossible to enrich with catalog context. This provides the shared shell:
// role=dialog + aria-modal, Esc-to-close, a backdrop, focus moved in on open and
// restored on close, and a Tab focus trap so keyboard users can't tab out into
// the (inert) page behind it.

import { useCallback, useEffect, useRef } from "react";
import { useTranslations } from "next-intl";

type Props = {
  open: boolean;
  title: string;
  onClose: () => void;
  children: React.ReactNode;
  /** Accessible id for the title element, wired to aria-labelledby. */
  titleId?: string;
};

const FOCUSABLE =
  'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])';

export function Dialog({ open, title, onClose, children, titleId = "dialog-title" }: Props) {
  const t = useTranslations("dialog");
  const panelRef = useRef<HTMLDivElement>(null);
  // The element focused before the dialog opened, so focus can be restored on close.
  const restoreRef = useRef<HTMLElement | null>(null);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== "Tab") return;
      const panel = panelRef.current;
      if (!panel) return;
      const items = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (items.length === 0) {
        // Nothing focusable inside — keep focus on the panel itself.
        e.preventDefault();
        panel.focus();
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || active === panel)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    },
    [onClose]
  );

  useEffect(() => {
    if (!open) return;
    restoreRef.current = document.activeElement as HTMLElement | null;
    const panel = panelRef.current;
    // Focus the first focusable control, else the panel itself.
    const firstFocusable = panel?.querySelector<HTMLElement>(FOCUSABLE);
    (firstFocusable ?? panel)?.focus();
    return () => {
      // Restore focus to the trigger when the dialog unmounts/closes.
      restoreRef.current?.focus?.();
    };
  }, [open]);

  if (!open) return null;

  return (
    <div
      // Backdrop: a click outside the panel dismisses.
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(0,0,0,0.4)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        zIndex: 1100,
        padding: "1rem",
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
        style={{
          background: "#fff",
          borderRadius: "0.5rem",
          boxShadow: "0 10px 30px rgba(0,0,0,0.25)",
          maxWidth: "32rem",
          width: "100%",
          maxHeight: "85vh",
          overflowY: "auto",
          outline: "none",
        }}
      >
        <div style={{ padding: "1rem 1.25rem", borderBottom: "1px solid #e5e7eb", display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <h2 id={titleId} style={{ fontSize: "1.05rem", fontWeight: 700, margin: 0 }}>
            {title}
          </h2>
          <button
            onClick={onClose}
            aria-label={t("close")}
            style={{ border: "none", background: "transparent", fontSize: "1.25rem", lineHeight: 1, cursor: "pointer", color: "#6b7280" }}
          >
            ×
          </button>
        </div>
        <div style={{ padding: "1.25rem" }}>{children}</div>
      </div>
    </div>
  );
}

/** Shared button style for dialog footers. */
export function DialogButton({
  label,
  onClick,
  variant = "default",
  disabled = false,
  autoFocus = false,
}: {
  label: string;
  onClick: () => void;
  variant?: "default" | "primary" | "danger";
  disabled?: boolean;
  autoFocus?: boolean;
}) {
  const styles: Record<string, { bg: string; border: string; fg: string }> = {
    default: { bg: "#fff", border: "#d1d5db", fg: "#111" },
    primary: { bg: "#2563eb", border: "#2563eb", fg: "#fff" },
    danger: { bg: "#dc2626", border: "#dc2626", fg: "#fff" },
  };
  const c = styles[variant];
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      autoFocus={autoFocus}
      style={{
        padding: "0.4rem 0.9rem",
        fontSize: "0.85rem",
        cursor: disabled ? "not-allowed" : "pointer",
        opacity: disabled ? 0.5 : 1,
        border: `1px solid ${c.border}`,
        borderRadius: "0.3rem",
        background: c.bg,
        color: c.fg,
        fontWeight: 600,
      }}
    >
      {label}
    </button>
  );
}
