// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { renderWithIntl } from "@/lib/i18n/test-utils";

// The sign-in form uses the app-router search params and next-auth's provider
// discovery; both are stubbed so the page renders in isolation.
vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams(""),
}));
vi.mock("next-auth/react", () => ({
  getProviders: async () => ({ basic: { id: "basic", name: "Basic", type: "credentials" } }),
  signIn: vi.fn(),
}));

import SignInPage from "./page";

describe("SignInPage accessibility (#43)", () => {
  it("labels the username and password inputs (previously placeholder-only)", async () => {
    renderWithIntl(<SignInPage />);

    // These controls used to carry only a placeholder (no accessible name).
    // They are now queryable by their accessible name via <label>/aria-label.
    await waitFor(() => expect(screen.getByLabelText("Username")).toBeDefined());
    expect(screen.getByLabelText("Password")).toBeDefined();
  });

  it("renders the localized heading from the catalog", async () => {
    const { unmount } = renderWithIntl(<SignInPage />, "en");
    await waitFor(() => expect(screen.getByRole("heading", { name: "Sign in" })).toBeDefined());
    unmount();

    renderWithIntl(<SignInPage />, "ja");
    await waitFor(() => expect(screen.getByRole("heading", { name: "サインイン" })).toBeDefined());
  });
});
