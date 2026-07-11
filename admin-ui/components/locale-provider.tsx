// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Client locale provider (#43). Wraps next-intl's NextIntlClientProvider in
// provider-only mode (no i18n URL routing, so the existing auth middleware is
// untouched) and exposes a manual language switch via useLocale().
//
// SSR-safety: both the server render and the first client render use
// DEFAULT_LOCALE, so hydration matches. The persisted/browser preference is
// resolved in an effect and applied immediately after mount.

import { createContext, useContext, useEffect, useState } from "react";
import { NextIntlClientProvider } from "next-intl";
import { MESSAGES } from "@/lib/i18n/messages";
import { DEFAULT_LOCALE, detectInitialLocale, persistLocale, type Locale } from "@/lib/i18n/locale";

type LocaleContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
};

const LocaleContext = createContext<LocaleContextValue | null>(null);

/** Read + change the active locale. Throws if used outside a LocaleProvider. */
export function useLocale(): LocaleContextValue {
  const ctx = useContext(LocaleContext);
  if (!ctx) throw new Error("useLocale must be used within a LocaleProvider");
  return ctx;
}

export function LocaleProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(DEFAULT_LOCALE);

  // Resolve the real preference (persisted choice > browser) after mount.
  useEffect(() => {
    setLocaleState(detectInitialLocale());
  }, []);

  // Keep the document language attribute in sync for assistive tech.
  useEffect(() => {
    if (typeof document !== "undefined") document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = (next: Locale) => {
    persistLocale(next);
    setLocaleState(next);
  };

  return (
    <LocaleContext.Provider value={{ locale, setLocale }}>
      {/* timeZone is set explicitly (we don't use next-intl date formatting)
          so it doesn't fall back to the ambient environment during SSR/SSG. */}
      <NextIntlClientProvider locale={locale} messages={MESSAGES[locale]} timeZone="UTC">
        {children}
      </NextIntlClientProvider>
    </LocaleContext.Provider>
  );
}
