// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { NextRequest, NextResponse } from "next/server";

/**
 * RP-initiated logout: after signOut() clears the local NextAuth session, the
 * Nav component's callbackUrl routes here with the (now-gone-from-session)
 * id_token carried as a query param, so we can still redirect to Keycloak's
 * end-session endpoint with it as id_token_hint — ending the SSO session too.
 * Basic-auth mode (no id_token, no KEYCLOAK_ISSUER) falls back to the app root.
 */
export function GET(req: NextRequest) {
  const idToken = req.nextUrl.searchParams.get("id_token");
  const issuer = process.env.KEYCLOAK_ISSUER;
  const appUrl = process.env.NEXTAUTH_URL ?? req.nextUrl.origin;

  if (!idToken || !issuer) {
    return NextResponse.redirect(appUrl);
  }

  const endSession = new URL(`${issuer}/protocol/openid-connect/logout`);
  endSession.searchParams.set("id_token_hint", idToken);
  endSession.searchParams.set("post_logout_redirect_uri", appUrl);
  return NextResponse.redirect(endSession.toString());
}
