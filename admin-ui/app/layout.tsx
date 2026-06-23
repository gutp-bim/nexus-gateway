// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import type { Metadata } from "next";
import { Providers } from "@/components/providers";
import { Nav } from "@/components/nav";

export const metadata: Metadata = {
  title: "nexus-gateway admin",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body style={{ margin: 0, fontFamily: "system-ui, sans-serif", background: "#f9fafb" }}>
        <Providers>
          <Nav />
          <main style={{ padding: "1.5rem 2rem" }}>{children}</main>
        </Providers>
      </body>
    </html>
  );
}
