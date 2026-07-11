// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { Suspense, useEffect, useState } from "react";
import { getProviders, signIn, type ClientSafeProvider } from "next-auth/react";
import { useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";

// Minimal custom sign-in page (issue #38): NextAuth's built-in default page
// has no concept of "your session expired" — it only surfaces its own
// OAuth-flow error codes. Strings are catalog-driven (EN/JA) as of #43.
export default function SignInPage() {
  // useSearchParams() opts this subtree out of static prerendering unless
  // wrapped in Suspense (Next.js requirement for App Router).
  return (
    <Suspense fallback={null}>
      <SignInForm />
    </Suspense>
  );
}

function SignInForm() {
  const t = useTranslations("signin");
  const params = useSearchParams();
  const expired = params.get("reason") === "expired";
  const callbackUrl = params.get("callbackUrl") ?? "/dashboard";
  const [providers, setProviders] = useState<Record<string, ClientSafeProvider> | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  useEffect(() => {
    getProviders().then(setProviders);
  }, []);

  return (
    <div style={{ maxWidth: 360, margin: "4rem auto" }}>
      <h1 style={{ fontSize: "1.25rem", fontWeight: 700, marginBottom: "1rem" }}>{t("title")}</h1>
      {expired && (
        <p style={{ color: "#dc2626", marginBottom: "1rem" }}>
          {t("expired")}
        </p>
      )}
      {providers?.keycloak && (
        <button
          onClick={() => signIn("keycloak", { callbackUrl })}
          style={{
            padding: "0.5rem 1rem",
            border: "1px solid #2563eb",
            borderRadius: "0.25rem",
            background: "#2563eb",
            color: "#fff",
            cursor: "pointer",
          }}
        >
          {t("keycloak")}
        </button>
      )}
      {providers?.basic && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            signIn("basic", { username, password, callbackUrl });
          }}
          style={{ display: "flex", flexDirection: "column", gap: "0.5rem" }}
        >
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder={t("username")}
            aria-label={t("username")}
            style={{ padding: "0.4rem 0.6rem", border: "1px solid #d1d5db", borderRadius: "0.25rem" }}
          />
          <input
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            type="password"
            placeholder={t("password")}
            aria-label={t("password")}
            style={{ padding: "0.4rem 0.6rem", border: "1px solid #d1d5db", borderRadius: "0.25rem" }}
          />
          <button
            type="submit"
            style={{
              padding: "0.5rem 1rem",
              border: "1px solid #2563eb",
              borderRadius: "0.25rem",
              background: "#2563eb",
              color: "#fff",
              cursor: "pointer",
            }}
          >
            {t("submit")}
          </button>
        </form>
      )}
    </div>
  );
}
