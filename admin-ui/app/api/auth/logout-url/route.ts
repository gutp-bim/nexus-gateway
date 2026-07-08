// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getToken } from "next-auth/jwt";
import { NextRequest, NextResponse } from "next/server";

/**
 * Resolves the RP-initiated (Keycloak) logout URL server-side, from the
 * still-live session JWT — the id_token never needs to reach client-side JS
 * or the browser's address bar/history for this app's own domain.
 *
 * Nav.tsx calls this BEFORE signOut(): getToken() reads the httpOnly session
 * cookie directly (like next-auth/middleware's own check), independent of
 * the `session()` callback, so it works even though the id_token is
 * deliberately not included in what `session()` exposes to the client (see
 * lib/auth.ts). Once signOut() clears the session, this would no longer have
 * anything to read — hence resolving the URL first, ahead of time.
 *
 * Basic-auth mode (no id_token, no KEYCLOAK_ISSUER) falls back to the app root.
 */
export async function GET(req: NextRequest) {
  const token = await getToken({ req });
  const idToken = token?.idToken as string | undefined;
  const issuer = process.env.KEYCLOAK_ISSUER;
  const appUrl = process.env.NEXTAUTH_URL ?? req.nextUrl.origin;

  if (!idToken || !issuer) {
    return NextResponse.json({ url: appUrl });
  }

  const endSession = new URL(`${issuer}/protocol/openid-connect/logout`);
  endSession.searchParams.set("id_token_hint", idToken);
  endSession.searchParams.set("post_logout_redirect_uri", appUrl);
  return NextResponse.json({ url: endSession.toString() });
}
