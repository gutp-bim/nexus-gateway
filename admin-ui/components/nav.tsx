// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { signOut, useSession } from "next-auth/react";
import Link from "next/link";
import { usePathname } from "next/navigation";

// Ends the Keycloak SSO session too (RP-initiated logout), not just the local
// NextAuth session — otherwise the next Login on a shared terminal silently
// re-authenticates as the previous operator. In Basic-auth mode there's no
// idToken, so signOut() alone (its default callbackUrl "/") is sufficient.
function handleLogout(idToken: string | undefined) {
  const callbackUrl = idToken
    ? `/api/auth/federated-logout?id_token=${encodeURIComponent(idToken)}`
    : "/";
  signOut({ callbackUrl });
}

export function Nav() {
  const { data: session } = useSession();
  const path = usePathname();

  return (
    <nav style={{ display: "flex", alignItems: "center", gap: "1rem", padding: "0.75rem 1.5rem", borderBottom: "1px solid #e5e7eb", background: "#fff" }}>
      <span style={{ fontWeight: 700, marginRight: "1rem" }}>nexus-gateway</span>
      <Link href="/dashboard" style={{ fontWeight: path === "/dashboard" ? 700 : 400 }}>Dashboard</Link>
      <Link href="/connectors" style={{ fontWeight: path === "/connectors" ? 700 : 400 }}>Connectors</Link>
      <Link href="/catalog" style={{ fontWeight: path === "/catalog" ? 700 : 400 }}>Catalog</Link>
      <Link href="/devices" style={{ fontWeight: path === "/devices" ? 700 : 400 }}>Devices</Link>
      <Link href="/telemetry" style={{ fontWeight: path === "/telemetry" ? 700 : 400 }}>Telemetry</Link>
      <Link href="/logs" style={{ fontWeight: path === "/logs" ? 700 : 400 }}>Logs</Link>
      <span style={{ marginLeft: "auto", fontSize: "0.875rem", color: "#6b7280" }}>{session?.user?.email}</span>
      <button onClick={() => handleLogout(session?.idToken)} style={{ cursor: "pointer" }}>Logout</button>
    </nav>
  );
}
