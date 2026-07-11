// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import type { ReactElement } from "react";
import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithIntl } from "@/lib/i18n/test-utils";
import { ToastProvider, useToast } from "./toast";

// ToastViewport reads catalog strings (variant words, dismiss aria-label).
const render = (ui: ReactElement) => renderWithIntl(ui);

function Trigger() {
  const toast = useToast();
  return (
    <>
      <button onClick={() => toast.success("saved ok")}>ok</button>
      <button onClick={() => toast.error("it failed")}>fail</button>
    </>
  );
}

describe("ToastProvider / useToast", () => {
  it("shows a toast and dismisses it via the close button", () => {
    render(
      <ToastProvider>
        <Trigger />
      </ToastProvider>
    );

    fireEvent.click(screen.getByText("ok"));
    expect(screen.getByText("saved ok")).toBeDefined();

    fireEvent.click(screen.getByLabelText("Dismiss notification"));
    expect(screen.queryByText("saved ok")).toBeNull();
  });

  it("renders an error toast with role=alert", () => {
    render(
      <ToastProvider>
        <Trigger />
      </ToastProvider>
    );
    fireEvent.click(screen.getByText("fail"));
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("it failed");
  });

  it("useToast throws outside a provider (wiring guard)", () => {
    function Bad() {
      useToast();
      return null;
    }
    // Suppress React's expected error logging for this intentional throw.
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(() => render(<Bad />)).toThrow(/ToastProvider/);
    spy.mockRestore();
  });
});
