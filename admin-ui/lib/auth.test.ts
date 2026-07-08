// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  authOptions,
  buildProviders,
  refreshAccessToken,
  resolveAuthProvider,
  verifyBasicCredentials,
} from "./auth";

/** Builds a fake JWT string (header.payload.signature) carrying the given realm roles. */
function fakeAccessToken(roles: string[]): string {
  const payload = Buffer.from(JSON.stringify({ realm_access: { roles } })).toString("base64url");
  return `header.${payload}.signature`;
}

describe("resolveAuthProvider", () => {
  it("defaults to basic when AUTH_PROVIDER is unset", () => {
    expect(resolveAuthProvider({})).toBe("basic");
  });

  it("selects keycloak when AUTH_PROVIDER=keycloak", () => {
    expect(resolveAuthProvider({ AUTH_PROVIDER: "keycloak" })).toBe("keycloak");
  });

  it("is case-insensitive", () => {
    expect(resolveAuthProvider({ AUTH_PROVIDER: "KeyCloak" })).toBe("keycloak");
  });

  it("falls back to basic for any unrecognized value", () => {
    expect(resolveAuthProvider({ AUTH_PROVIDER: "ldap" })).toBe("basic");
  });
});

describe("verifyBasicCredentials", () => {
  const env = { ADMIN_USERNAME: "admin", ADMIN_PASSWORD: "s3cr3t" };

  it("accepts matching username and password", async () => {
    const user = await verifyBasicCredentials({ username: "admin", password: "s3cr3t" }, env);
    expect(user).toMatchObject({ id: "admin", name: "admin", realmRoles: ["gateway-operator"] });
  });

  it("rejects a wrong password", async () => {
    const user = await verifyBasicCredentials({ username: "admin", password: "wrong" }, env);
    expect(user).toBeNull();
  });

  it("rejects a wrong username", async () => {
    const user = await verifyBasicCredentials({ username: "someone-else", password: "s3cr3t" }, env);
    expect(user).toBeNull();
  });

  it("rejects a password that is a prefix of the real one (length mismatch)", async () => {
    const user = await verifyBasicCredentials({ username: "admin", password: "s3cr3" }, env);
    expect(user).toBeNull();
  });

  it("rejects missing credentials", async () => {
    expect(await verifyBasicCredentials(undefined, env)).toBeNull();
    expect(await verifyBasicCredentials({ username: "", password: "" }, env)).toBeNull();
    expect(await verifyBasicCredentials({ username: "admin", password: "" }, env)).toBeNull();
  });

  it("defaults the expected username to 'admin' when ADMIN_USERNAME is unset", async () => {
    const user = await verifyBasicCredentials(
      { username: "admin", password: "s3cr3t" },
      { ADMIN_PASSWORD: "s3cr3t" }
    );
    expect(user).toMatchObject({ id: "admin" });
  });

  it("fails closed — rejects every login when ADMIN_PASSWORD is not configured", async () => {
    const user = await verifyBasicCredentials({ username: "admin", password: "admin" }, {});
    expect(user).toBeNull();
  });

  it("fails closed even when the submitted password is the literal empty string and ADMIN_PASSWORD is also unset", async () => {
    const user = await verifyBasicCredentials({ username: "admin", password: "" }, {});
    expect(user).toBeNull();
  });
});

describe("buildProviders", () => {
  it("returns a single Basic-auth credentials provider by default", () => {
    const providers = buildProviders({ ADMIN_PASSWORD: "s3cr3t" });
    expect(providers).toHaveLength(1);
    expect(providers[0].type).toBe("credentials");
    // next-auth's CredentialsProvider() factory always self-reports
    // `id: "credentials"` at this layer; NextAuth's request pipeline merges
    // in our custom "basic" id at runtime (see core/lib/providers.js). The
    // raw options we passed in — including our chosen id — survive verbatim
    // on `.options`, so that's what's actually checkable here.
    expect((providers[0] as { options?: { id?: string } }).options?.id).toBe("basic");
  });

  it("returns a single Keycloak provider when AUTH_PROVIDER=keycloak", () => {
    const providers = buildProviders({
      AUTH_PROVIDER: "keycloak",
      KEYCLOAK_ID: "admin-ui",
      KEYCLOAK_SECRET: "admin-ui-secret",
      KEYCLOAK_ISSUER: "http://localhost:8090/realms/nexus-gateway",
    });
    expect(providers).toHaveLength(1);
    expect(providers[0].id).toBe("keycloak");
  });
});

describe("authOptions.callbacks.jwt", () => {
  const jwt = authOptions.callbacks!.jwt!;
  // Only the fields the callback actually reads are supplied; the rest of
  // each param's real shape isn't relevant to this callback's behavior.
  const call = (args: Record<string, unknown>) => jwt(args as Parameters<typeof jwt>[0]);

  it("Keycloak sign-in: persists the access/id token and derives roles from it", async () => {
    const accessToken = fakeAccessToken(["gateway-operator"]);
    const token = await call({
      token: {},
      account: { access_token: accessToken, id_token: "id-tok" },
      user: { id: "u1" },
    });
    expect(token.accessToken).toBe(accessToken);
    expect(token.idToken).toBe("id-tok");
    expect(token.realmRoles).toEqual(["gateway-operator"]);
  });

  it("Keycloak refresh (no account/user, only the persisted token): still re-derives roles from token.accessToken", async () => {
    // Regression test: an earlier refactor accidentally nested role
    // re-derivation inside `if (account?.access_token)`, so it only ran on
    // the initial sign-in and went stale on every later call — NextAuth only
    // passes `account`/`user` on that first call, not on subsequent ones.
    const updatedToken = fakeAccessToken(["gateway-operator", "viewer"]);
    const token = await call({
      // realmRoles carries a stale value; accessToken carries the "current"
      // one — asserting against the latter proves re-derivation happened
      // rather than the stale value simply passing through untouched.
      token: { accessToken: updatedToken, realmRoles: ["viewer"] },
      account: undefined,
      user: undefined,
    });
    expect(token.realmRoles).toEqual(["gateway-operator", "viewer"]);
  });

  it("Basic auth sign-in: takes roles from the authorize() result, sets no accessToken", async () => {
    const token = await call({
      token: {},
      account: undefined,
      user: { id: "admin", name: "admin", realmRoles: ["gateway-operator"] },
    });
    expect(token.accessToken).toBeUndefined();
    expect(token.realmRoles).toEqual(["gateway-operator"]);
  });

  it("Basic auth subsequent call: leaves the persisted roles untouched (no accessToken to re-derive from)", async () => {
    const token = await call({
      token: { realmRoles: ["gateway-operator"] },
      account: undefined,
      user: undefined,
    });
    expect(token.accessToken).toBeUndefined();
    expect(token.realmRoles).toEqual(["gateway-operator"]);
  });

  it("propagates a persisted refresh error through to the token", async () => {
    const token = await call({
      token: { accessToken: fakeAccessToken(["gateway-operator"]), error: "RefreshAccessTokenError" },
      account: undefined,
      user: undefined,
    });
    expect(token.error).toBe("RefreshAccessTokenError");
  });

  it("Keycloak sign-in clears a prior refresh error, so re-authenticating after being bounced to /auth/signin?reason=expired doesn't loop", async () => {
    const accessToken = fakeAccessToken(["gateway-operator"]);
    const token = await call({
      token: { error: "RefreshAccessTokenError" },
      account: { access_token: accessToken, id_token: "id-tok" },
      user: { id: "u1" },
    });
    expect(token.error).toBeUndefined();
  });

  it("a stale token (past expiry) with a valid refresh token yields a refreshed accessToken and no error", async () => {
    const oldToken = fakeAccessToken(["gateway-operator"]);
    const newToken = fakeAccessToken(["gateway-operator", "gateway-viewer"]);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ access_token: newToken, expires_in: 300 }),
      })
    );
    try {
      const token = await call({
        token: {
          accessToken: oldToken,
          refreshToken: "refresh-1",
          expiresAt: Math.floor(Date.now() / 1000) - 10, // already expired
        },
        account: undefined,
        user: undefined,
      });
      expect(token.accessToken).toBe(newToken);
      expect(token.error).toBeUndefined();
      expect(token.realmRoles).toEqual(["gateway-operator", "gateway-viewer"]);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe("refreshAccessToken", () => {
  const env = {
    KEYCLOAK_ISSUER: "http://localhost:8090/realms/nexus-gateway",
    KEYCLOAK_ID: "admin-ui",
    KEYCLOAK_SECRET: "admin-ui-secret",
  };

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("posts grant_type=refresh_token with the persisted refresh token to the Keycloak token endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ access_token: "new-access", expires_in: 300 }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await refreshAccessToken({ refreshToken: "refresh-1" }, env);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("http://localhost:8090/realms/nexus-gateway/protocol/openid-connect/token");
    const body = init.body as URLSearchParams;
    expect(body.get("grant_type")).toBe("refresh_token");
    expect(body.get("refresh_token")).toBe("refresh-1");
    expect(body.get("client_id")).toBe("admin-ui");
  });

  it("on success: sets a new accessToken/expiresAt and clears any prior error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ access_token: "new-access", expires_in: 300 }),
      })
    );

    const before = Math.floor(Date.now() / 1000);
    const result = await refreshAccessToken(
      { refreshToken: "refresh-1", error: "RefreshAccessTokenError" },
      env
    );

    expect(result.accessToken).toBe("new-access");
    expect(result.error).toBeUndefined();
    expect(result.expiresAt as number).toBeGreaterThanOrEqual(before + 300);
  });

  it("rotates the refresh token when the response includes a new one", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ access_token: "new-access", refresh_token: "refresh-2", expires_in: 300 }),
      })
    );

    const result = await refreshAccessToken({ refreshToken: "refresh-1" }, env);
    expect(result.refreshToken).toBe("refresh-2");
  });

  it("keeps the old refresh token when the response omits a new one", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ access_token: "new-access", expires_in: 300 }),
      })
    );

    const result = await refreshAccessToken({ refreshToken: "refresh-1" }, env);
    expect(result.refreshToken).toBe("refresh-1");
  });

  it("marks the token errored when the token endpoint responds non-2xx", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 400 }));

    const result = await refreshAccessToken({ refreshToken: "refresh-1" }, env);
    expect(result.error).toBe("RefreshAccessTokenError");
  });

  it("marks the token errored when fetch itself throws (network failure)", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("network down")));

    const result = await refreshAccessToken({ refreshToken: "refresh-1" }, env);
    expect(result.error).toBe("RefreshAccessTokenError");
  });

  it("marks the token errored when a 2xx response omits access_token (malformed body treated as failure, not cached as success)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: async () => ({ expires_in: 300 }) })
    );

    const result = await refreshAccessToken({ refreshToken: "refresh-1" }, env);
    expect(result.error).toBe("RefreshAccessTokenError");
  });

  it("single-flights concurrent refreshes of the same refresh token into one Keycloak request", async () => {
    // Keycloak rotates refresh tokens by default: two concurrent redeems of
    // the same not-yet-rotated token would otherwise race, and the second to
    // reach Keycloak would fail against an already-consumed token.
    let resolveFetch!: (v: unknown) => void;
    const fetchMock = vi.fn().mockReturnValue(
      new Promise((resolve) => {
        resolveFetch = resolve;
      })
    );
    vi.stubGlobal("fetch", fetchMock);

    const p1 = refreshAccessToken({ refreshToken: "refresh-shared" }, env);
    const p2 = refreshAccessToken({ refreshToken: "refresh-shared" }, env);

    resolveFetch({ ok: true, json: async () => ({ access_token: "new-access", expires_in: 300 }) });
    const [r1, r2] = await Promise.all([p1, p2]);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(r1.accessToken).toBe("new-access");
    expect(r2.accessToken).toBe("new-access");
  });

  it("does not single-flight refreshes for different refresh tokens", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ access_token: "new-access", expires_in: 300 }),
    });
    vi.stubGlobal("fetch", fetchMock);

    await Promise.all([
      refreshAccessToken({ refreshToken: "refresh-a" }, env),
      refreshAccessToken({ refreshToken: "refresh-b" }, env),
    ]);

    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
