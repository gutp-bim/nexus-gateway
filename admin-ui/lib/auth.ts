// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import type { NextAuthOptions } from "next-auth";
import KeycloakProvider from "next-auth/providers/keycloak";

function decodeRealmRoles(rawToken: string): string[] {
  const parts = rawToken.split(".");
  if (parts.length < 2) return [];
  try {
    const payload = JSON.parse(Buffer.from(parts[1], "base64url").toString());
    return (payload?.realm_access?.roles ?? []) as string[];
  } catch {
    return [];
  }
}

export const authOptions: NextAuthOptions = {
  providers: [
    KeycloakProvider({
      clientId: process.env.KEYCLOAK_ID!,
      clientSecret: process.env.KEYCLOAK_SECRET!,
      issuer: process.env.KEYCLOAK_ISSUER!,
      // Allow server-side OIDC discovery to use the Docker-internal hostname while
      // the browser-facing issuer URL (used for iss validation) stays as localhost.
      wellKnown: process.env.KEYCLOAK_INTERNAL_ISSUER
        ? `${process.env.KEYCLOAK_INTERNAL_ISSUER}/.well-known/openid-configuration`
        : undefined,
    }),
  ],
  callbacks: {
    async jwt({ token, account }) {
      // Persist the access_token so API routes can forward it to the Admin API.
      if (account) {
        token.accessToken = account.access_token;
        token.idToken = account.id_token;
      }
      // Always re-derive realm roles from the current access token so that
      // a token refresh picks up any role changes without re-login.
      const rawToken = (token.accessToken as string | undefined);
      if (rawToken) {
        token.realmRoles = decodeRealmRoles(rawToken);
      }
      return token;
    },
    async session({ session, token }) {
      session.accessToken = token.accessToken as string | undefined;
      session.realmRoles = (token.realmRoles ?? []) as string[];
      return session;
    },
  },
};

declare module "next-auth" {
  interface Session {
    accessToken?: string;
    realmRoles: string[];
  }
}
