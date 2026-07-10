// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/apiClient";
import { ErrorBanner, messageFor } from "./error-banner";

describe("messageFor", () => {
  it("prefers ApiError/Error message, falls back to String", () => {
    expect(messageFor(new ApiError("network", "Could not reach the server."))).toBe(
      "Could not reach the server."
    );
    expect(messageFor(new Error("boom"))).toBe("boom");
    expect(messageFor("raw string")).toBe("raw string");
  });
});

describe("ErrorBanner", () => {
  it("renders the human-readable message and fires onRetry", () => {
    const err = new ApiError("bad_gateway", "Gateway is unreachable. Try again shortly.", 502);
    const onRetry = vi.fn();
    render(<ErrorBanner error={err} onRetry={onRetry} label="Failed to load" />);

    expect(screen.getByText(/Gateway is unreachable/)).toBeDefined();
    fireEvent.click(screen.getByText("Retry"));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("omits the Retry button when no onRetry is given", () => {
    render(<ErrorBanner error={new Error("x")} />);
    expect(screen.queryByText("Retry")).toBeNull();
  });
});
