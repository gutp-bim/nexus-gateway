// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom
// apiClient.ts is browser-only (uses window.location, dynamic-imports
// next-auth/react) — the suite's default "node" environment has no window.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiFetch, ApiError, isArrayOf, isRecord } from "./apiClient";

const signInMock = vi.fn();
vi.mock("next-auth/react", () => ({
  signIn: (...args: unknown[]) => signInMock(...args),
}));

function mockFetchOnce(res: Partial<Response> & { ok: boolean; status: number }) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      json: async () => ({}),
      ...res,
    })
  );
}

describe("apiFetch — status mapping", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    signInMock.mockClear();
  });

  const cases: Array<[number, string]> = [
    [401, "unauthorized"],
    [403, "forbidden"],
    [404, "not_found"],
    [500, "server_error"],
    [502, "bad_gateway"],
    [503, "bad_gateway"],
    [504, "bad_gateway"],
    [418, "server_error"], // unmapped status falls back to server_error
  ];

  for (const [status, kind] of cases) {
    it(`maps ${status} -> ${kind}`, async () => {
      mockFetchOnce({ ok: false, status });
      await expect(apiFetch("/x")).rejects.toMatchObject({ kind, status });
    });
  }
});

describe("apiFetch — 401 sign-in trigger", () => {
  // apiClient.ts's sign-in dedupe flag is module-level state, so each test
  // here needs a fresh module instance (vi.resetModules + dynamic re-import)
  // rather than sharing the statically-imported apiFetch used elsewhere in
  // this file — otherwise an earlier 401 in another test leaves signIn
  // permanently "already triggered" for the rest of the suite.
  beforeEach(() => {
    vi.resetModules();
    signInMock.mockClear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("triggers signIn on 401", async () => {
    mockFetchOnce({ ok: false, status: 401 });
    const fresh = await import("./apiClient");
    await expect(fresh.apiFetch("/x")).rejects.toBeInstanceOf(fresh.ApiError);
    // signIn() is dynamically imported — flush the microtask queue.
    await new Promise((r) => setTimeout(r, 0));
    expect(signInMock).toHaveBeenCalledTimes(1);
    expect(signInMock).toHaveBeenCalledWith(undefined, expect.objectContaining({ callbackUrl: expect.any(String) }));
  });

  it("does not trigger signIn on 403 (RBAC role mismatch isn't fixed by re-auth)", async () => {
    mockFetchOnce({ ok: false, status: 403 });
    const fresh = await import("./apiClient");
    await expect(fresh.apiFetch("/x")).rejects.toBeInstanceOf(fresh.ApiError);
    await new Promise((r) => setTimeout(r, 0));
    expect(signInMock).not.toHaveBeenCalled();
  });

  it("does not call signIn twice for two concurrent 401s (dedupe)", async () => {
    mockFetchOnce({ ok: false, status: 401 });
    const fresh = await import("./apiClient");
    await Promise.all([
      fresh.apiFetch("/x").catch(() => {}),
      fresh.apiFetch("/y").catch(() => {}),
    ]);
    await new Promise((r) => setTimeout(r, 0));
    expect(signInMock).toHaveBeenCalledTimes(1);
  });

  it("resets the dedupe guard when signIn() itself rejects, so a later 401 can retry", async () => {
    // Regression guard: a failed redirect (chunk-load error, offline) must
    // not leave 401 handling permanently disabled for the rest of the tab.
    signInMock.mockRejectedValueOnce(new Error("chunk load failed"));
    mockFetchOnce({ ok: false, status: 401 });
    const fresh = await import("./apiClient");

    await expect(fresh.apiFetch("/x")).rejects.toBeInstanceOf(fresh.ApiError);
    await new Promise((r) => setTimeout(r, 0));
    expect(signInMock).toHaveBeenCalledTimes(1);

    await expect(fresh.apiFetch("/y")).rejects.toBeInstanceOf(fresh.ApiError);
    await new Promise((r) => setTimeout(r, 0));
    expect(signInMock).toHaveBeenCalledTimes(2);
  });
});

describe("apiFetch — response handling", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("returns undefined for a 204 response without parsing JSON", async () => {
    const jsonSpy = vi.fn();
    mockFetchOnce({ ok: true, status: 204, json: jsonSpy });
    const result = await apiFetch("/x");
    expect(result).toBeUndefined();
    expect(jsonSpy).not.toHaveBeenCalled();
  });

  it("returns parsed JSON when no guard is supplied", async () => {
    mockFetchOnce({ ok: true, status: 200, json: async () => ({ a: 1 }) });
    const result = await apiFetch("/x");
    expect(result).toEqual({ a: 1 });
  });

  it("throws invalid_response when JSON parsing fails", async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: async () => {
        throw new Error("bad json");
      },
    });
    await expect(apiFetch("/x")).rejects.toMatchObject({ kind: "invalid_response" });
  });

  it("throws invalid_response when the guard rejects the shape", async () => {
    mockFetchOnce({ ok: true, status: 200, json: async () => ({ not: "an array" }) });
    await expect(apiFetch("/x", undefined, isArrayOf())).rejects.toMatchObject({ kind: "invalid_response" });
  });

  it("returns the value when the guard accepts the shape", async () => {
    mockFetchOnce({ ok: true, status: 200, json: async () => [1, 2, 3] });
    const result = await apiFetch("/x", undefined, isArrayOf());
    expect(result).toEqual([1, 2, 3]);
  });

  it("throws network when fetch itself rejects", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
    await expect(apiFetch("/x")).rejects.toMatchObject({ kind: "network" });
  });
});

describe("isArrayOf / isRecord", () => {
  it("isArrayOf accepts an array with no item guard", () => {
    expect(isArrayOf()([1, 2, 3])).toBe(true);
    expect(isArrayOf()("not an array")).toBe(false);
  });

  it("isArrayOf rejects when an item guard fails on any element", () => {
    const isNumber = (x: unknown): x is number => typeof x === "number";
    expect(isArrayOf(isNumber)([1, 2, 3])).toBe(true);
    expect(isArrayOf(isNumber)([1, "2", 3])).toBe(false);
  });

  it("isRecord accepts a plain object and rejects arrays/null", () => {
    expect(isRecord({ a: 1 })).toBe(true);
    expect(isRecord([1, 2])).toBe(false);
    expect(isRecord(null)).toBe(false);
  });
});
