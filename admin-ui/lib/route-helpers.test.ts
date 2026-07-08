// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import type { Session } from "next-auth";
import { sessionGuard, withAdminApi } from "./route-helpers";
import { AdminApiError } from "./api";

const validSession: Session = {
  accessToken: "tok",
  realmRoles: ["gateway-operator"],
  expires: "2099-01-01T00:00:00.000Z",
};

describe("sessionGuard", () => {
  it("returns a 401 when there is no session", async () => {
    const res = sessionGuard(null)!;
    expect(res.status).toBe(401);
    expect(await res.json()).toEqual({ error: "session_expired" });
  });

  it("returns a 401 when the session is errored (refresh failed)", async () => {
    const res = sessionGuard({ ...validSession, error: "RefreshAccessTokenError" })!;
    expect(res.status).toBe(401);
  });

  it("returns null for a valid, non-errored session", () => {
    expect(sessionGuard(validSession)).toBeNull();
  });
});

describe("withAdminApi", () => {
  it("returns a 401 without calling work() when the session guard fails", async () => {
    const res = await withAdminApi(null, () => {
      throw new Error("must not be called");
    });
    expect(res.status).toBe(401);
  });

  it("returns 200 JSON for a resolved value", async () => {
    const res = await withAdminApi(validSession, async () => ({ hello: "world" }));
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ hello: "world" });
  });

  it("returns 204 with an empty body when work() resolves undefined", async () => {
    const res = await withAdminApi(validSession, async () => undefined);
    expect(res.status).toBe(204);
  });

  it("passes a 401 AdminApiError through as-is", async () => {
    const res = await withAdminApi(validSession, async () => {
      throw new AdminApiError(401, "Unauthorized", "/x");
    });
    expect(res.status).toBe(401);
  });

  it("passes a 403 AdminApiError through as-is", async () => {
    const res = await withAdminApi(validSession, async () => {
      throw new AdminApiError(403, "Forbidden", "/x");
    });
    expect(res.status).toBe(403);
  });

  it("maps a 500 AdminApiError to 502", async () => {
    const res = await withAdminApi(validSession, async () => {
      throw new AdminApiError(500, "Internal Server Error", "/x");
    });
    expect(res.status).toBe(502);
  });

  it("maps a non-AdminApiError error to 502", async () => {
    const res = await withAdminApi(validSession, async () => {
      throw new Error("boom");
    });
    expect(res.status).toBe(502);
  });
});
