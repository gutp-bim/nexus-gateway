// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from "vitest";
import { AdminApiError, listCatalog } from "./api";

describe("AdminApiError", () => {
  it("carries status, statusText, and body", () => {
    const err = new AdminApiError(401, "Unauthorized", "/catalog", "unauthorized");
    expect(err.status).toBe(401);
    expect(err.statusText).toBe("Unauthorized");
    expect(err.body).toBe("unauthorized");
    expect(err.message).toBe("Admin API /catalog: 401 Unauthorized");
    expect(err.name).toBe("AdminApiError");
  });
});

describe("adminFetch-driven functions throw AdminApiError", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("listCatalog throws AdminApiError (not a plain Error) on a non-ok response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 401,
        statusText: "Unauthorized",
        text: async () => "unauthorized",
      })
    );
    await expect(listCatalog("tok")).rejects.toBeInstanceOf(AdminApiError);
  });
});
