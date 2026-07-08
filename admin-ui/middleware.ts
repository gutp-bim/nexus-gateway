// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import withAuth from "next-auth/middleware";

// A visitor with no session cookie at all is redirected by the default
// `authorized` check (token truthy). A session with a persisted refresh
// failure (see lib/auth.ts's jwt() callback) is NOT a token the default
// check would reject — token is still present, just marked errored — so it
// is called out explicitly here as a second, defensive layer. The primary
// mechanism that catches a stale-but-not-yet-known-failed token is the
// proactive refresh in getServerSession()/useSession(), not this middleware:
// next-auth/middleware's getToken() reads the JWT cookie directly and does
// NOT re-invoke jwt(), so it can only see an error already written to the
// cookie by an earlier refresh attempt elsewhere.
export default withAuth({
  callbacks: {
    authorized: ({ token }) => !!token && token.error !== "RefreshAccessTokenError",
  },
  pages: { signIn: "/auth/signin" },
});

export const config = {
  // Protect all routes except NextAuth internals, API proxy routes (handle
  // own auth), the custom sign-in page itself (excluding it is required —
  // otherwise an unauthenticated visit to /auth/signin redirects back to
  // /auth/signin, an infinite loop), and static assets.
  matcher: ["/((?!api/auth|api/gateway|auth/signin|_next/static|_next/image|favicon.ico).*)"],
};
