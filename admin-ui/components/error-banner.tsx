// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// In-place error banner (#46). Consumes the typed errors from lib/apiClient —
// ApiError already carries a human-readable message (the MESSAGES map), so a
// caller renders it directly instead of dumping a raw technical string, and
// gets a retry affordance for load failures.

import { useTranslations } from "next-intl";
import { ApiError } from "@/lib/apiClient";

/** Human-readable text for any caught value (ApiError message, Error message, else String). */
export function messageFor(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}

export function ErrorBanner({
  error,
  onRetry,
  label,
}: {
  error: unknown;
  onRetry?: () => void;
  label?: string;
}) {
  const t = useTranslations("errorBanner");
  const message = messageFor(error);
  return (
    <div
      role="alert"
      style={{
        display: "flex",
        alignItems: "center",
        gap: "0.75rem",
        padding: "0.625rem 0.875rem",
        background: "#fef2f2",
        border: "1px solid #fecaca",
        borderRadius: "0.375rem",
        color: "#991b1b",
        fontSize: "0.875rem",
      }}
    >
      <span aria-hidden="true">⚠</span>
      <span style={{ flex: 1 }}>
        {label ? `${label}: ` : ""}
        {message}
      </span>
      {onRetry && (
        <button
          onClick={onRetry}
          style={{
            border: "1px solid #dc2626",
            background: "#fff",
            color: "#dc2626",
            borderRadius: "0.25rem",
            padding: "0.25rem 0.625rem",
            cursor: "pointer",
            fontSize: "0.8125rem",
          }}
        >
          {t("retry")}
        </button>
      )}
    </div>
  );
}
