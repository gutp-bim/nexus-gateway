// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import ErrorPage from "./error";

describe("route error boundary", () => {
  it("renders a recoverable page with the message and calls reset", () => {
    const reset = vi.fn();
    render(<ErrorPage error={new Error("kaboom")} reset={reset} />);

    expect(screen.getByText(/Something went wrong/)).toBeDefined();
    expect(screen.getByText("kaboom")).toBeDefined();

    fireEvent.click(screen.getByText("Try again"));
    expect(reset).toHaveBeenCalledTimes(1);
  });

  it("falls back to a generic message when the error has none", () => {
    render(<ErrorPage error={new Error("")} reset={() => {}} />);
    expect(screen.getByText(/unexpected error/i)).toBeDefined();
  });
});
