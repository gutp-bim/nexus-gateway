// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { signOut, useSession } from "next-auth/react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useTranslations } from "next-intl";
import { LanguageSwitcher } from "@/components/language-switcher";

// Ends the Keycloak SSO session too (RP-initiated logout), not just the local
// NextAuth session — otherwise the next Login on a shared terminal silently
// re-authenticates as the previous operator. In Basic-auth mode there's no
// id_token, so /api/auth/logout-url resolves to the app root and this is
// equivalent to a plain signOut().
//
// The Keycloak end-session URL is resolved server-side (GET
// /api/auth/logout-url, before signOut() clears the session) rather than
// building it here from a client-held id_token: the id_token is deliberately
// not exposed via useSession() (see lib/auth.ts), and routing it through this
// app's own URL/history (as an earlier version of this flow did, via
// ?id_token=...) would leak it into browser history and server access logs
// for no benefit — next-auth's signOut({ callbackUrl }) is restricted to
// same-origin URLs anyway, so the final cross-origin hop to Keycloak has to
// happen as a manual redirect regardless.
async function handleLogout() {
  const res = await fetch("/api/auth/logout-url");
  const { url } = await res.json();
  await signOut({ redirect: false });
  window.location.href = url;
}

export function Nav() {
  const { data: session } = useSession();
  const path = usePathname();
  const t = useTranslations("nav");

  return (
    <nav style={{ display: "flex", alignItems: "center", gap: "1rem", padding: "0.75rem 1.5rem", borderBottom: "1px solid #e5e7eb", background: "#fff" }}>
      <span style={{ fontWeight: 700, marginRight: "1rem" }}>{t("brand")}</span>
      <Link href="/dashboard" style={{ fontWeight: path === "/dashboard" ? 700 : 400 }}>{t("dashboard")}</Link>
      <Link href="/connectors" style={{ fontWeight: path === "/connectors" ? 700 : 400 }}>{t("connectors")}</Link>
      <Link href="/catalog" style={{ fontWeight: path === "/catalog" ? 700 : 400 }}>{t("catalog")}</Link>
      <Link href="/devices" style={{ fontWeight: path === "/devices" ? 700 : 400 }}>{t("devices")}</Link>
      <Link href="/telemetry" style={{ fontWeight: path === "/telemetry" ? 700 : 400 }}>{t("telemetry")}</Link>
      <Link href="/logs" style={{ fontWeight: path === "/logs" ? 700 : 400 }}>{t("logs")}</Link>
      <span style={{ marginLeft: "auto", fontSize: "0.875rem", color: "#6b7280" }}>{session?.user?.email}</span>
      <LanguageSwitcher />
      <button onClick={() => handleLogout()} style={{ cursor: "pointer" }}>{t("logout")}</button>
    </nav>
  );
}
