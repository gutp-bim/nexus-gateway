// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { buildProviders, resolveAuthProvider, verifyBasicCredentials } from "./auth";

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
