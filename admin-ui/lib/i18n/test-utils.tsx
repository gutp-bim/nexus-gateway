// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// Test helper (#43): renders a component tree inside the i18n + locale context
// so components that call useTranslations()/useLocale() work in unit tests.
// Defaults to English (matching the canonical catalog); pass a locale to assert
// the translated path.

import type { ReactElement, ReactNode } from "react";
import { render, type RenderOptions } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";
import { LocaleProvider } from "@/components/locale-provider";
import { MESSAGES } from "@/lib/i18n/messages";
import type { Locale } from "@/lib/i18n/locale";

/** A bare next-intl provider at a fixed locale (no browser/persistence resolution). */
export function IntlWrapper({ locale = "en", children }: { locale?: Locale; children: ReactNode }) {
  return (
    <NextIntlClientProvider locale={locale} messages={MESSAGES[locale]} timeZone="UTC">
      {children}
    </NextIntlClientProvider>
  );
}

/** render() wrapped in a fixed-locale intl provider. */
export function renderWithIntl(ui: ReactElement, locale: Locale = "en", options?: RenderOptions) {
  return render(<IntlWrapper locale={locale}>{ui}</IntlWrapper>, options);
}

/** render() wrapped in the full LocaleProvider (resolves browser/persistence). */
export function renderWithLocaleProvider(ui: ReactElement, options?: RenderOptions) {
  return render(<LocaleProvider>{ui}</LocaleProvider>, options);
}
