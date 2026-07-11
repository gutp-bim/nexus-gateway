// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

// The message catalogs are the single source of truth for operator-facing text
// (#43). English is canonical; Japanese is the translation. Both are bundled
// (small admin app, no async locale loading) and keyed by locale.

import type { Locale } from "@/lib/i18n/locale";
import en from "@/messages/en.json";
import ja from "@/messages/ja.json";

export type Messages = typeof en;

export const MESSAGES: Record<Locale, Messages> = { en, ja };
