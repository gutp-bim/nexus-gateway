// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { renderWithIntl } from "@/lib/i18n/test-utils";
import LogsPage from "./page";

// Wrap in the default (English) intl provider so useTranslations() resolves.
const render = (ui: ReactElement) => renderWithIntl(ui);

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
      json: async () => ({ connector_id: "bacnet-01", lines: ["line one", "line two"] }),
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

// Structured (JSON) log lines. The INFO line's *message* deliberately contains
// the word "error" to prove severity is read from the parsed `level` field, not
// from a substring of the raw line.
const STRUCTURED_LINES = [
  JSON.stringify({ time: "2026-07-11T00:00:00Z", level: "INFO", msg: "recovered from error cleanly" }),
  JSON.stringify({ timestamp: "2026-07-11T00:00:01Z", level: "WARN", message: "disk near capacity" }),
  JSON.stringify({ time: "2026-07-11T00:00:02Z", level: "ERROR", msg: "connection refused" }),
  JSON.stringify({ time: "2026-07-11T00:00:03Z", level: "INFO", msg: "startup complete" }),
];

// Like stubLogFetch but the log source returns structured JSON lines. Records
// every requested URL so tests can assert which source was fetched.
function stubStructuredFetch() {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes("/connectors")) {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: async () => [{ id: "bacnet-01", image: "img", running: true }],
      });
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      json: async () => ({ connector_id: "gateway", lines: STRUCTURED_LINES }),
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

    // A source is selected on mount, so its logs are fetched once regardless of
    // the tail toggle. Non-JSON lines render verbatim.
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

  it("offers Gateway as a source and fetches /logs/gateway when it is selected (#42)", async () => {
    const fetchMock = stubStructuredFetch();
    render(<LogsPage />);

    const sourceSelect = screen.getByLabelText("Log source") as HTMLSelectElement;
    // Gateway is offered as an option even with connectors present.
    const optionValues = Array.from(sourceSelect.options).map((o) => o.value);
    expect(optionValues).toContain("gateway");

    // Switch to a connector, then back to Gateway, and confirm the gateway log
    // endpoint is requested.
    fireEvent.change(sourceSelect, { target: { value: "bacnet-01" } });
    await waitFor(() =>
      expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("/logs/bacnet-01"))).toBe(true)
    );

    fireEvent.change(sourceSelect, { target: { value: "gateway" } });
    await waitFor(() =>
      expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("/logs/gateway"))).toBe(true)
    );
  });

  it("filters by parsed `level`, not by substring: warnings-only hides an INFO line whose message contains 'error' (#42)", async () => {
    stubStructuredFetch();
    render(<LogsPage />);

    // All lines show under the default "All" filter, including the human-readable
    // message text of each structured line.
    await waitFor(() => expect(screen.getByText("recovered from error cleanly")).toBeDefined());
    expect(screen.getByText("disk near capacity")).toBeDefined();
    expect(screen.getByText("connection refused")).toBeDefined();
    expect(screen.getByText("startup complete")).toBeDefined();

    // Switch to warnings & errors only.
    const severitySelect = screen.getByLabelText("Severity filter") as HTMLSelectElement;
    fireEvent.change(severitySelect, { target: { value: "warn" } });

    await waitFor(() => {
      // WARN and ERROR lines remain.
      expect(screen.getByText("disk near capacity")).toBeDefined();
      expect(screen.getByText("connection refused")).toBeDefined();
    });
    // The INFO line whose *message text* contains "error" is HIDDEN — proving
    // severity comes from the JSON `level` field, not a substring match.
    expect(screen.queryByText("recovered from error cleanly")).toBeNull();
    // The plain INFO line is hidden too.
    expect(screen.queryByText("startup complete")).toBeNull();
  });

  it("labels both the source selector and the severity control (accessibility, #42)", async () => {
    stubStructuredFetch();
    render(<LogsPage />);

    expect(screen.getByLabelText("Log source")).toBeDefined();
    expect(screen.getByLabelText("Severity filter")).toBeDefined();
  });
});
