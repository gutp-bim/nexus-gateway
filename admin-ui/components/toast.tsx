// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Lightweight accessible toast system (#46). No dependency — a context provider
// plus a fixed viewport. Action outcomes (start/stop/install/update/…) surface
// here so a success/failure isn't easy to miss the way an inline banner is.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useTranslations } from "next-intl";

export type ToastVariant = "success" | "error" | "info";
export type Toast = { id: number; message: string; variant: ToastVariant };
export type ToastInput = { message: string; variant?: ToastVariant; durationMs?: number };

export type ToastApi = {
  show: (t: ToastInput) => void;
  success: (message: string) => void;
  error: (message: string) => void;
  info: (message: string) => void;
};

// Errors linger longer than confirmations — an operator needs time to read a
// failure. 0 would mean "sticky until dismissed".
const DEFAULT_DURATION_MS: Record<ToastVariant, number> = {
  success: 4000,
  info: 4000,
  error: 8000,
};

// Status is paired with a text glyph, never color alone, so the variant is
// distinguishable without color perception (accessibility, #46).
const GLYPH: Record<ToastVariant, string> = { success: "✓", error: "⚠", info: "ℹ" };
const COLORS: Record<ToastVariant, { bg: string; border: string; fg: string }> = {
  success: { bg: "#f0fdf4", border: "#16a34a", fg: "#166534" },
  error: { bg: "#fef2f2", border: "#dc2626", fg: "#991b1b" },
  info: { bg: "#eff6ff", border: "#2563eb", fg: "#1e40af" },
};

const ToastContext = createContext<ToastApi | null>(null);

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextId = useRef(1);
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>());

  const dismiss = useCallback((id: number) => {
    setToasts((cur) => cur.filter((t) => t.id !== id));
    const timer = timers.current.get(id);
    if (timer) {
      clearTimeout(timer);
      timers.current.delete(id);
    }
  }, []);

  const show = useCallback(
    (input: ToastInput) => {
      const id = nextId.current++;
      const variant = input.variant ?? "info";
      setToasts((cur) => [...cur, { id, message: input.message, variant }]);
      const duration = input.durationMs ?? DEFAULT_DURATION_MS[variant];
      if (duration > 0) {
        timers.current.set(
          id,
          setTimeout(() => dismiss(id), duration)
        );
      }
    },
    [dismiss]
  );

  // Clear any pending timers if the provider unmounts.
  useEffect(() => {
    const pending = timers.current;
    return () => {
      pending.forEach((t) => clearTimeout(t));
      pending.clear();
    };
  }, []);

  const api = useMemo<ToastApi>(
    () => ({
      show,
      success: (message) => show({ message, variant: "success" }),
      error: (message) => show({ message, variant: "error" }),
      info: (message) => show({ message, variant: "info" }),
    }),
    [show]
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <ToastViewport toasts={toasts} onDismiss={dismiss} />
    </ToastContext.Provider>
  );
}

/** Access the toast API. Throws if used outside a ToastProvider (a wiring bug). */
export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error("useToast must be used within a ToastProvider");
  return ctx;
}

function ToastViewport({ toasts, onDismiss }: { toasts: Toast[]; onDismiss: (id: number) => void }) {
  const t = useTranslations("toast");
  return (
    // aria-live=polite announces new toasts to screen readers without stealing
    // focus; individual error toasts escalate to role=alert below.
    <div
      aria-live="polite"
      style={{
        position: "fixed",
        top: "1rem",
        right: "1rem",
        display: "flex",
        flexDirection: "column",
        gap: "0.5rem",
        zIndex: 1000,
        maxWidth: "24rem",
      }}
    >
      {toasts.map((toast) => {
        const c = COLORS[toast.variant];
        return (
          <div
            key={toast.id}
            role={toast.variant === "error" ? "alert" : "status"}
            style={{
              display: "flex",
              alignItems: "flex-start",
              gap: "0.5rem",
              padding: "0.625rem 0.75rem",
              background: c.bg,
              border: `1px solid ${c.border}`,
              borderRadius: "0.375rem",
              color: c.fg,
              fontSize: "0.875rem",
              boxShadow: "0 1px 3px rgba(0,0,0,0.1)",
            }}
          >
            <span aria-hidden="true">{GLYPH[toast.variant]}</span>
            {/* Visually-hidden variant word so a screen reader hears
                "Success"/"Error"/"Info", not just the message + role urgency. */}
            <span
              style={{
                position: "absolute",
                width: 1,
                height: 1,
                overflow: "hidden",
                clip: "rect(0 0 0 0)",
                whiteSpace: "nowrap",
              }}
            >
              {t(toast.variant)}:
            </span>
            <span style={{ flex: 1 }}>{toast.message}</span>
            <button
              onClick={() => onDismiss(toast.id)}
              aria-label={t("dismiss")}
              style={{
                border: "none",
                background: "transparent",
                color: c.fg,
                cursor: "pointer",
                fontSize: "1rem",
                lineHeight: 1,
                padding: 0,
              }}
            >
              ×
            </button>
          </div>
        );
      })}
    </div>
  );
}
