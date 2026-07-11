// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// 404 page (#46) — replaces Next.js's default not-found so an unknown route
// lands on a labeled, localized page with a way back into the app. Client
// component so it can read the catalog via the shared LocaleProvider (#43).

import Link from "next/link";
import { useTranslations } from "next-intl";

export default function NotFound() {
  const t = useTranslations("notFound");
  return (
    <div style={{ maxWidth: "32rem", margin: "3rem auto", textAlign: "center" }}>
      <h2 style={{ marginBottom: "0.5rem" }}>{t("title")}</h2>
      <p style={{ color: "#6b7280", marginBottom: "1.5rem" }}>{t("body")}</p>
      <Link href="/dashboard" aria-label={t("link")} style={{ color: "#2563eb" }}>
        {t("link")}
      </Link>
    </div>
  );
}
