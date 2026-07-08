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

// Refresh a bit early so a request in flight doesn't race the literal expiry
// instant.
const REFRESH_SKEW_SECONDS = 30;

// In-flight refresh requests keyed by refresh token, so concurrent
// getServerSession()/API calls that race the same expiring token share one
// Keycloak round-trip instead of each redeeming it — Keycloak rotates
// refresh tokens by default, so a second concurrent redeem of the
// already-consumed token would fail and wrongly stamp the session with
// RefreshAccessTokenError even though the first request succeeded.
const inFlightRefreshes = new Map<string, Promise<Record<string, unknown>>>();

/**
 * Exchanges token.refreshToken for a new access token against Keycloak's
 * token endpoint (grant_type=refresh_token). On success returns the token
 * with refreshed accessToken/refreshToken/expiresAt and no error; on any
 * failure (non-2xx response, network error, malformed response body) returns
 * the token stamped with `error: "RefreshAccessTokenError"` so callers can
 * redirect to sign-in. Exported standalone so it is unit-testable without
 * going through NextAuth's request pipeline (fake clock + mocked fetch).
 */
export async function refreshAccessToken(
  token: Record<string, unknown>,
  env: EnvLike = process.env
): Promise<Record<string, unknown>> {
  const refreshToken = String(token.refreshToken ?? "");
  const inFlight = inFlightRefreshes.get(refreshToken);
  if (inFlight) return inFlight;

  const promise = doRefreshAccessToken(token, refreshToken, env).finally(() => {
    inFlightRefreshes.delete(refreshToken);
  });
  inFlightRefreshes.set(refreshToken, promise);
  return promise;
}

async function doRefreshAccessToken(
  token: Record<string, unknown>,
  refreshToken: string,
  env: EnvLike
): Promise<Record<string, unknown>> {
  try {
    const issuer = env.KEYCLOAK_INTERNAL_ISSUER || env.KEYCLOAK_ISSUER;
    const res = await fetch(`${issuer}/protocol/openid-connect/token`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "refresh_token",
        client_id: env.KEYCLOAK_ID ?? "",
        client_secret: env.KEYCLOAK_SECRET ?? "",
        refresh_token: refreshToken,
      }),
    });
    if (!res.ok) throw new Error(`refresh failed: ${res.status}`);
    const refreshed = await res.json();
    // A 200 with no access_token is malformed, not success — treat it the
    // same as a network/HTTP failure rather than caching an empty token.
    if (typeof refreshed.access_token !== "string" || !refreshed.access_token) {
      throw new Error("refresh response missing access_token");
    }
    return {
      ...token,
      accessToken: refreshed.access_token,
      // Keycloak rotates refresh tokens by default; keep the old one only
      // if the response omits a new one.
      refreshToken: refreshed.refresh_token ?? token.refreshToken,
      idToken: refreshed.id_token ?? token.idToken,
      expiresAt: Math.floor(Date.now() / 1000) + refreshed.expires_in,
      error: undefined,
    };
  } catch {
    return { ...token, error: "RefreshAccessTokenError" as const };
  }
}

export const authOptions: NextAuthOptions = {
  providers: buildProviders(),
  pages: { signIn: "/auth/signin" },
  callbacks: {
    async jwt({ token, account, user }) {
      if (account?.access_token) {
        // OIDC (Keycloak): persist the access_token so API routes can forward
        // it to the Admin API, plus what's needed to refresh it later.
        token.accessToken = account.access_token;
        token.idToken = account.id_token;
        token.refreshToken = account.refresh_token;
        token.expiresAt = account.expires_at; // epoch seconds, set by next-auth's OAuth client from expires_in
        // A fresh sign-in supersedes any error left over from a prior
        // refresh failure — without this, a user who re-authenticates after
        // being redirected to /auth/signin?reason=expired would still carry
        // error: "RefreshAccessTokenError" on the new token, and
        // middleware's `authorized` check would immediately bounce them
        // back to sign-in again (an inescapable loop).
        token.error = undefined;
      } else if (user) {
        // Credentials (Basic auth): there is no OIDC token to forward — the
        // Admin API runs open in this mode (no KEYCLOAK_JWKS_URL) — so roles
        // come straight from what authorize() already resolved.
        token.realmRoles = user.realmRoles ?? [];
      }

      if (!token.accessToken) return token; // Basic auth: nothing to refresh

      // NOTE: getServerSession() re-invokes this callback on every call for
      // JWT-strategy sessions (an API route calling it is itself a refresh
      // trigger), but next-auth/middleware's getToken() reads the JWT cookie
      // directly and does NOT re-invoke jwt() — middleware is a defensive
      // second layer, not where the refresh actually happens.
      //
      // expiresAt unset (unknown) is treated as still-valid rather than
      // forcing an unwanted refresh with no signal to act on.
      const stillValid =
        typeof token.expiresAt !== "number" ||
        Date.now() < (token.expiresAt - REFRESH_SKEW_SECONDS) * 1000;

      if (stillValid) {
        token.realmRoles = decodeRealmRoles(token.accessToken as string);
        return token;
      }

      if (!token.refreshToken) {
        return { ...token, error: "RefreshAccessTokenError" as const };
      }

      const refreshed = await refreshAccessToken(token);
      refreshed.realmRoles = refreshed.accessToken
        ? decodeRealmRoles(refreshed.accessToken as string)
        : token.realmRoles;
      return refreshed;
    },
    async session({ session, token }) {
      session.accessToken = token.accessToken as string | undefined;
      // idToken is deliberately NOT copied onto the client-visible session —
      // it's only needed server-side to build the Keycloak RP-initiated
      // logout URL (see app/api/auth/logout-url/route.ts, which reads it via
      // next-auth/jwt's getToken() instead). Exposing a raw OIDC ID token to
      // arbitrary client-side JS via useSession() has no legitimate consumer
      // in this app and only widens the token's exposure surface.
      session.realmRoles = (token.realmRoles ?? []) as string[];
      session.error = token.error as string | undefined;
      return session;
    },
  },
};

declare module "next-auth" {
  interface Session {
    accessToken?: string;
    realmRoles: string[];
    error?: string;
  }
  interface User {
    realmRoles?: string[];
  }
}

declare module "next-auth/jwt" {
  interface JWT {
    accessToken?: string;
    idToken?: string;
    refreshToken?: string;
    expiresAt?: number;
    realmRoles?: string[];
    error?: string;
  }
}
