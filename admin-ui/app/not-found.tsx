// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import Link from "next/link";

// 404 page (#46) — replaces Next.js's default not-found so an unknown route
// lands on a labeled page with a way back into the app.
export default function NotFound() {
  return (
    <div style={{ maxWidth: "32rem", margin: "3rem auto", textAlign: "center" }}>
      <h2 style={{ marginBottom: "0.5rem" }}>Page not found</h2>
      <p style={{ color: "#6b7280", marginBottom: "1.5rem" }}>
        The page you&apos;re looking for doesn&apos;t exist.
      </p>
      <Link href="/dashboard" style={{ color: "#2563eb" }}>
        Go to dashboard
      </Link>
    </div>
  );
}
