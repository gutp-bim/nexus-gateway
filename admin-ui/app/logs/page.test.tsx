// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import LogsPage from "./page";

vi.mock("next-auth/react", () => ({
  signIn: vi.fn(),
}));

// Routes a mocked fetch by URL so the connector-list load and the per-connector
// log fetch return their own shapes.
function stubLogFetch() {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes("/connectors")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: async () => [{ id: "bacnet-01", image: "img", running: true }],
      });
    }
    // /logs/{id}
    return Promise.resolve({
      ok: true,
      status: 200,
      json: async () => ({ id: "bacnet-01", lines: ["line one", "line two"] }),
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

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

  it("loads the selected connector's log tail on mount and offers an auto-refresh toggle (#41)", async () => {
    const fetchMock = stubLogFetch();
    render(<LogsPage />);

    // Selecting the first connector (auto-selected on mount) fetches its logs
    // once, regardless of the tail toggle.
    await waitFor(() => expect(screen.getByText("line one")).toBeDefined());
    const logCallsBefore = fetchMock.mock.calls.filter((c) => String(c[0]).includes("/logs/")).length;
    expect(logCallsBefore).toBeGreaterThanOrEqual(1);

    // The auto-refresh (tail) toggle exists and starts off.
    const toggle = screen.getByRole("checkbox") as HTMLInputElement;
    expect(toggle.checked).toBe(false);

    // Turning it on triggers additional polling of the log tail.
    fireEvent.click(toggle);
    await waitFor(() => {
      const after = fetchMock.mock.calls.filter((c) => String(c[0]).includes("/logs/")).length;
      expect(after).toBeGreaterThan(logCallsBefore);
    });
  });
});
