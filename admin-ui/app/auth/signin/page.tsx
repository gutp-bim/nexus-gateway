// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { Suspense, useEffect, useState } from "react";
import { getProviders, signIn, type ClientSafeProvider } from "next-auth/react";
import { useSearchParams } from "next/navigation";

// Minimal custom sign-in page (issue #38): NextAuth's built-in default page
// has no concept of "your session expired" — it only surfaces its own
// OAuth-flow error codes. Deliberately small: no design-system or i18n work
// here (out of scope for #38/#39; see PRD #17's broader UI slices).
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
      <h1 style={{ fontSize: "1.25rem", fontWeight: 700, marginBottom: "1rem" }}>Sign in</h1>
      {expired && (
        <p style={{ color: "#dc2626", marginBottom: "1rem" }}>
          Your session has expired. Please sign in again.
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
          Sign in with Keycloak
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
            placeholder="Username"
            style={{ padding: "0.4rem 0.6rem", border: "1px solid #d1d5db", borderRadius: "0.25rem" }}
          />
          <input
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            type="password"
            placeholder="Password"
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
            Sign in
          </button>
        </form>
      )}
    </div>
  );
}
