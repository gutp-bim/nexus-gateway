// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import {
  DEFAULT_LOCALE,
  detectInitialLocale,
  isLocale,
  persistLocale,
  readStoredLocale,
  resolveLocale,
  STORAGE_KEY,
} from "./locale";

describe("resolveLocale", () => {
  it("maps a ja* browser language to Japanese, everything else to English", () => {
    expect(resolveLocale(null, "ja")).toBe("ja");
    expect(resolveLocale(null, "ja-JP")).toBe("ja");
    expect(resolveLocale(null, "en-US")).toBe("en");
    expect(resolveLocale(null, "fr")).toBe("en");
    expect(resolveLocale(null, null)).toBe("en");
    expect(resolveLocale(null, undefined)).toBe("en");
  });

  it("lets a persisted choice win over the browser default", () => {
    // Browser says Japanese, but the operator picked English.
    expect(resolveLocale("en", "ja-JP")).toBe("en");
    // Browser says English, but the operator picked Japanese.
    expect(resolveLocale("ja", "en-US")).toBe("ja");
  });

  it("ignores an invalid persisted value and falls back to the browser", () => {
    expect(resolveLocale("fr", "ja-JP")).toBe("ja");
    expect(resolveLocale("", "en-US")).toBe("en");
  });
});

describe("isLocale", () => {
  it("accepts only supported locales", () => {
    expect(isLocale("en")).toBe(true);
    expect(isLocale("ja")).toBe(true);
    expect(isLocale("fr")).toBe(false);
    expect(isLocale(null)).toBe(false);
  });
});

describe("persistence (localStorage)", () => {
  afterEach(() => {
    localStorage.clear();
  });

  it("round-trips the persisted locale under the versioned key", () => {
    expect(readStoredLocale()).toBeNull();
    persistLocale("ja");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("ja");
    expect(readStoredLocale()).toBe("ja");
  });

  it("detectInitialLocale prefers the persisted choice over the browser", () => {
    // navigator.language in jsdom is an en-* value, so absent persistence the
    // default is English…
    expect(detectInitialLocale()).toBe(DEFAULT_LOCALE);
    // …and a persisted Japanese choice overrides that on the next load.
    persistLocale("ja");
    expect(detectInitialLocale()).toBe("ja");
  });
});
