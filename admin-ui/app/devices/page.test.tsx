// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { renderWithIntl } from "@/lib/i18n/test-utils";
import DevicesPage from "./page";

const render = (ui: ReactElement) => renderWithIntl(ui);

const POINTS = [
  { point_id: "p-1", local_id: "l-1", connector_id: "bacnet-01", protocol: "bacnet", unit: "°C", writable: true },
  { point_id: "p-2", local_id: "l-2", connector_id: "bacnet-01", protocol: "bacnet", unit: "%", writable: false },
];

function stubFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ ok: true, status: 200, json: async () => POINTS }))
  );
}

describe("DevicesPage accessibility (#43)", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("exposes the writable status as text, not the color-coded glyph alone", async () => {
    stubFetch();
    render(<DevicesPage />);

    // Both status states are surfaced as words alongside the ✓ / — glyph, so
    // the meaning survives without color perception.
    await waitFor(() => expect(screen.getByText("Read-only")).toBeDefined());
    // "Writable" appears both as the column header and the writable-row status;
    // at least the status cell must be present in addition to the header.
    expect(screen.getAllByText("Writable").length).toBeGreaterThanOrEqual(2);
  });

  it("renders the localized heading in Japanese", async () => {
    stubFetch();
    renderWithIntl(<DevicesPage />, "ja");
    await waitFor(() => expect(screen.getByText("デバイスとポイント")).toBeDefined());
  });
});
