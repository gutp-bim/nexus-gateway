// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import LogsPage from "./page";

vi.mock("next-auth/react", () => ({
  signIn: vi.fn(),
}));

describe("LogsPage", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders a readable error instead of crashing when the connector-list call fails (401)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 401,
        statusText: "Unauthorized",
        text: async () => "unauthorized",
        json: async () => ({ error: "unauthorized" }),
      })
    );

    render(<LogsPage />);

    // Regression guard for the pre-#39 crash: an error-object payload
    // (`{error: "..."}`) used to be set directly into `connectors` state with
    // no shape guard, so the later `connectors.map(...)` render threw
    // "connectors.map is not a function" — a white screen, not this message.
    // getByText itself throws if no matching node is found, so a passing
    // call already proves the readable error rendered (no jest-dom needed).
    await waitFor(() => {
      expect(screen.getByText(/Your session has expired/i)).toBeDefined();
    });
  });
});
