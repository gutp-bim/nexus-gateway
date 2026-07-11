// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import TelemetryPage from "./page";

vi.mock("next-auth/react", () => ({ signIn: vi.fn() }));

const EXTENDED = {
  received: 1000,
  sent: 990,
  accepted: 988,
  buffer_depth: 5,
  dropped: 0,
  checkpoints: 20,
  send_errors: 0,
  drifts: { "p-001": 2 },
  drift_total: 2,
  uplink_connected: true,
  last_checkpoint_unix: Math.floor(Date.now() / 1000) - 10,
  events_stream: { msgs: 4321, bytes: 987654 },
};

function stubFetch(telemetry: Record<string, unknown>) {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes("/telemetry")) {
      return Promise.resolve({ ok: true, status: 200, json: async () => telemetry });
    }
    // /recent
    return Promise.resolve({ ok: true, status: 200, json: async () => ({ values: [] }) });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("TelemetryPage", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("renders the extended pipeline figures and EVENTS stream usage (#47)", async () => {
    stubFetch(EXTENDED);
    render(<TelemetryPage />);

    await waitFor(() => expect(screen.getByText("Received")).toBeDefined());
    // received/sent/accepted values are localeString-formatted.
    expect(screen.getByText("1,000")).toBeDefined();
    expect(screen.getByText("990")).toBeDefined();
    expect(screen.getByText("988")).toBeDefined();
    // Uplink health surfaces as text, not just a color.
    expect(screen.getByText("Connected")).toBeDefined();
    // EVENTS stream usage renders when present.
    expect(screen.getByText("EVENTS Stream")).toBeDefined();
    expect(screen.getByText("4,321")).toBeDefined();
  });

  it("flags a disconnected uplink", async () => {
    stubFetch({ ...EXTENDED, uplink_connected: false });
    render(<TelemetryPage />);
    await waitFor(() => expect(screen.getByText("Disconnected")).toBeDefined());
  });

  // An older gateway (pre-#47) returns only buffer_depth + drifts. The screen must
  // degrade gracefully (no crash), showing 0/"never"/"Unknown" rather than throwing.
  it("degrades gracefully on an old payload shape", async () => {
    stubFetch({ buffer_depth: 7, drifts: { "p-001": 3 } });
    render(<TelemetryPage />);

    await waitFor(() => expect(screen.getByText("Received")).toBeDefined());
    // Uplink state is unknowable → neutral "Unknown", not a red "Disconnected".
    expect(screen.getByText("Unknown")).toBeDefined();
    // Total Drift falls back to summing the per-point map (3), matching the table.
    expect(screen.getByText("Last Checkpoint")).toBeDefined();
    expect(screen.getByText("never")).toBeDefined();
  });
});
