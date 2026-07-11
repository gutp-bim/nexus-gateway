// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Locale resolution for the admin-ui (#43). Provider-only i18n (no URL routing):
// the active locale is resolved entirely on the client from a persisted choice
// or the browser preference, so there is no server/client hydration mismatch.

export const LOCALES = ["en", "ja"] as const;
export type Locale = (typeof LOCALES)[number];

// The default used for SSR and the first client render (before the effect that
// resolves the real preference runs). English is the canonical catalog.
export const DEFAULT_LOCALE: Locale = "en";

// Versioned key so a future change to the stored shape can invalidate cleanly.
export const STORAGE_KEY = "adminui.locale.v1";

export function isLocale(value: unknown): value is Locale {
  return value === "en" || value === "ja";
}

/**
 * Pure resolution: a persisted choice wins; otherwise the browser language
 * (`ja*` → Japanese, everything else → English). Kept side-effect-free so it is
 * unit-testable without touching `localStorage`/`navigator`.
 */
export function resolveLocale(stored: string | null, navLang: string | null | undefined): Locale {
  if (isLocale(stored)) return stored;
  const lang = (navLang ?? "").toLowerCase();
  return lang.startsWith("ja") ? "ja" : "en";
}

/** Reads the persisted locale, tolerating a missing/blocked `localStorage`. */
export function readStoredLocale(): string | null {
  try {
    return typeof localStorage !== "undefined" ? localStorage.getItem(STORAGE_KEY) : null;
  } catch {
    return null;
  }
}

/** Persists the chosen locale, tolerating a missing/blocked `localStorage`. */
export function persistLocale(locale: Locale): void {
  try {
    localStorage?.setItem(STORAGE_KEY, locale);
  } catch {
    /* storage unavailable (private mode / SSR) — persistence is best-effort */
  }
}

/** Resolves the initial client locale from persistence + the browser. */
export function detectInitialLocale(): Locale {
  const navLang = typeof navigator !== "undefined" ? navigator.language : null;
  return resolveLocale(readStoredLocale(), navLang);
}
