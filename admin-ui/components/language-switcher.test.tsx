// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useTranslations } from "next-intl";
import { LocaleProvider } from "./locale-provider";
import { LanguageSwitcher } from "./language-switcher";
import { STORAGE_KEY } from "@/lib/i18n/locale";

// A tiny consumer that renders a translated string so we can watch it flip
// languages when the switcher changes locale.
function Probe() {
  const t = useTranslations("nav");
  return <span data-testid="probe">{t("dashboard")}</span>;
}

function setNavigatorLanguage(lang: string) {
  Object.defineProperty(window.navigator, "language", { value: lang, configurable: true });
}

describe("LanguageSwitcher + LocaleProvider", () => {
  afterEach(() => {
    localStorage.clear();
    setNavigatorLanguage("en-US");
  });

  it("defaults to English for an en browser and to Japanese for a ja browser", async () => {
    setNavigatorLanguage("en-US");
    const { unmount } = render(
      <LocaleProvider>
        <Probe />
      </LocaleProvider>
    );
    await waitFor(() => expect(screen.getByTestId("probe").textContent).toBe("Dashboard"));
    unmount();

    setNavigatorLanguage("ja-JP");
    render(
      <LocaleProvider>
        <Probe />
      </LocaleProvider>
    );
    await waitFor(() => expect(screen.getByTestId("probe").textContent).toBe("ダッシュボード"));
  });

  it("re-renders a visible string in the other language when toggled, and persists the choice", async () => {
    setNavigatorLanguage("en-US");
    render(
      <LocaleProvider>
        <LanguageSwitcher />
        <Probe />
      </LocaleProvider>
    );

    // Starts English.
    await waitFor(() => expect(screen.getByTestId("probe").textContent).toBe("Dashboard"));
    const select = screen.getByLabelText("Language") as HTMLSelectElement;
    expect(select.value).toBe("en");

    // Switch to Japanese: the visible translated string flips.
    act(() => {
      fireEvent.change(select, { target: { value: "ja" } });
    });
    await waitFor(() => expect(screen.getByTestId("probe").textContent).toBe("ダッシュボード"));

    // The choice is persisted under the versioned key.
    expect(localStorage.getItem(STORAGE_KEY)).toBe("ja");
  });

  it("honours a persisted override over the browser default on reload", async () => {
    // Browser prefers English, but a prior session persisted Japanese.
    setNavigatorLanguage("en-US");
    localStorage.setItem(STORAGE_KEY, "ja");
    render(
      <LocaleProvider>
        <Probe />
      </LocaleProvider>
    );
    await waitFor(() => expect(screen.getByTestId("probe").textContent).toBe("ダッシュボード"));
  });
});
