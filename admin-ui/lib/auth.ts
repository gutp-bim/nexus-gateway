// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { createHash, timingSafeEqual } from "crypto";
import type { NextAuthOptions } from "next-auth";
import CredentialsProvider from "next-auth/providers/credentials";
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

// The single local operator account (Basic auth mode) is granted the same
// role a Keycloak "operator" realm role would carry, so role-gated UI (e.g.
// catalog/connectors pages) behaves identically regardless of auth provider.
const BASIC_AUTH_ROLE = "gateway-operator";

/**
 * Constant-time string compare so a wrong password doesn't leak length or
 * prefix via timing. Hashing first means both inputs to timingSafeEqual are
 * always the same (digest) length, so there's no early-return-on-length-
 * mismatch branch that would itself leak the real password's length.
 */
function timingSafeStringEqual(a: string, b: string): boolean {
  const aHash = createHash("sha256").update(a).digest();
  const bHash = createHash("sha256").update(b).digest();
  return timingSafeEqual(aHash, bHash);
}

type BasicAuthUser = { id: string; name: string; realmRoles: string[] };

// Deliberately looser than NodeJS.ProcessEnv (which requires NODE_ENV etc.) —
// these functions only ever read a handful of named keys, and tests should be
// able to pass a bare partial-env object without satisfying the full interface.
type EnvLike = Record<string, string | undefined>;

/**
 * Verifies HTTP-Basic-style credentials against ADMIN_USERNAME/ADMIN_PASSWORD.
 * Exported standalone (rather than inlined in the Credentials provider) so it
 * is unit-testable without going through NextAuth's request pipeline.
 */
export function verifyBasicCredentials(
  credentials: Record<"username" | "password", string> | undefined,
  env: EnvLike = process.env
): BasicAuthUser | null {
  const expectedPassword = env.ADMIN_PASSWORD;
  // Fail closed: without an explicitly configured ADMIN_PASSWORD, reject every
  // login attempt rather than falling back to some guessable default.
  if (!expectedPassword) return null;
  if (!credentials?.username || !credentials.password) return null;

  const expectedUsername = env.ADMIN_USERNAME || "admin";
  if (credentials.username !== expectedUsername) return null;
  if (!timingSafeStringEqual(credentials.password, expectedPassword)) return null;

  return { id: expectedUsername, name: expectedUsername, realmRoles: [BASIC_AUTH_ROLE] };
}

/** "basic" (default, no external IdP) or "keycloak" (opt-in SSO) — see FEAT-046. */
export function resolveAuthProvider(env: EnvLike = process.env): "basic" | "keycloak" {
  return env.AUTH_PROVIDER?.toLowerCase() === "keycloak" ? "keycloak" : "basic";
}

export function buildProviders(env: EnvLike = process.env) {
  if (resolveAuthProvider(env) === "keycloak") {
    return [
      KeycloakProvider({
        clientId: env.KEYCLOAK_ID!,
        clientSecret: env.KEYCLOAK_SECRET!,
        issuer: env.KEYCLOAK_ISSUER!,
        // Allow server-side OIDC discovery to use the Docker-internal hostname while
        // the browser-facing issuer URL (used for iss validation) stays as localhost.
        wellKnown: env.KEYCLOAK_INTERNAL_ISSUER
          ? `${env.KEYCLOAK_INTERNAL_ISSUER}/.well-known/openid-configuration`
          : undefined,
      }),
    ];
  }
  return [
    CredentialsProvider({
      id: "basic",
      name: "Basic Auth",
      credentials: {
        username: { label: "Username", type: "text" },
        password: { label: "Password", type: "password" },
      },
      authorize: (credentials) => verifyBasicCredentials(credentials, env),
    }),
  ];
}

export const authOptions: NextAuthOptions = {
  providers: buildProviders(),
  callbacks: {
    async jwt({ token, account, user }) {
      if (account?.access_token) {
        // OIDC (Keycloak): persist the access_token so API routes can forward
        // it to the Admin API, and always re-derive realm roles from the
        // current access token so a refresh picks up role changes without re-login.
        token.accessToken = account.access_token;
        token.idToken = account.id_token;
        token.realmRoles = decodeRealmRoles(token.accessToken as string);
      } else if (user) {
        // Credentials (Basic auth): there is no OIDC token to forward — the
        // Admin API runs open in this mode (no KEYCLOAK_JWKS_URL) — so roles
        // come straight from what authorize() already resolved.
        token.realmRoles = user.realmRoles ?? [];
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
  interface User {
    realmRoles?: string[];
  }
}
