// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ConfirmDialog } from "./confirm-dialog";

describe("ConfirmDialog", () => {
  it("renders nothing when closed", () => {
    render(
      <ConfirmDialog open={false} title="Stop x" message="Stop it?" confirmLabel="Stop" onConfirm={vi.fn()} onCancel={vi.fn()} />
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("fires onConfirm only when the confirm button is clicked", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Stop mqtt-01"
        message="Stop connector mqtt-01?"
        confirmLabel="Stop"
        danger
        onConfirm={onConfirm}
        onCancel={onCancel}
      />
    );

    expect(screen.getByRole("dialog")).toBeDefined();
    expect(screen.getByText("Stop connector mqtt-01?")).toBeDefined();

    fireEvent.click(screen.getByText("Stop"));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("fires onCancel (not onConfirm) when cancelled", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog open title="Stop x" message="Stop it?" confirmLabel="Stop" onConfirm={onConfirm} onCancel={onCancel} />
    );

    fireEvent.click(screen.getByText("Cancel"));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("closes via Escape", () => {
    const onCancel = vi.fn();
    render(
      <ConfirmDialog open title="Stop x" message="Stop it?" confirmLabel="Stop" onConfirm={vi.fn()} onCancel={onCancel} />
    );
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });
});
