// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Manual language switcher (#43). Persists the choice (versioned localStorage
// key) via useLocale(); the selection takes precedence over the browser default
// on the next load.

import { useTranslations } from "next-intl";
import { useLocale } from "@/components/locale-provider";
import { isLocale } from "@/lib/i18n/locale";

export function LanguageSwitcher() {
  const { locale, setLocale } = useLocale();
  const t = useTranslations("language");

  return (
    <label style={{ display: "flex", alignItems: "center", gap: "0.35rem", fontSize: "0.8rem", color: "#6b7280" }}>
      <span>{t("label")}</span>
      <select
        aria-label={t("label")}
        value={locale}
        onChange={(e) => {
          const next = e.target.value;
          if (isLocale(next)) setLocale(next);
        }}
        style={{ padding: "0.2rem 0.4rem", borderRadius: "0.25rem", border: "1px solid #d1d5db", fontSize: "0.8rem" }}
      >
        <option value="en">{t("english")}</option>
        <option value="ja">{t("japanese")}</option>
      </select>
    </label>
  );
}
