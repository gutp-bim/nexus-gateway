// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

export { default as middleware } from "next-auth/middleware";

export const config = {
  // Protect all routes except NextAuth internals, API proxy routes (handle own auth), and static assets.
  matcher: ["/((?!api/auth|api/gateway|_next/static|_next/image|favicon.ico).*)"],
};
