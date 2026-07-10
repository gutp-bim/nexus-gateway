// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { CatalogEntry } from "@/lib/api";
import { UpgradeDialog, validateImageRef } from "./upgrade-dialog";

const PINNED = "ghcr.io/acme/mqtt@sha256:" + "a".repeat(64);

const catalogEntry: CatalogEntry = {
  name: "mqtt-01",
  version: "1.4.2",
  image: "ghcr.io/acme/mqtt:1.4.2",
  digest: "sha256:" + "b".repeat(64),
  min_gateway_version: "0.1.0",
  signature_required: true,
};

describe("validateImageRef", () => {
  it("rejects empty or whitespace-containing refs", () => {
    expect(validateImageRef("").valid).toBe(false);
    expect(validateImageRef("   ").valid).toBe(false);
    expect(validateImageRef("ghcr.io/x y").valid).toBe(false);
  });

  it("accepts a digest-pinned ref with no warning", () => {
    const r = validateImageRef(PINNED);
    expect(r.valid).toBe(true);
    expect(r.warning).toBeUndefined();
  });

  it("accepts a tag-only ref but warns it is not digest-pinned", () => {
    const r = validateImageRef("ghcr.io/acme/mqtt:1.4.2");
    expect(r.valid).toBe(true);
    expect(r.warning).toBeDefined();
  });
});

describe("UpgradeDialog", () => {
  it("lists the catalog version and fires onUpdate", () => {
    const onUpdate = vi.fn();
    render(
      <UpgradeDialog
        open
        connectorId="mqtt-01"
        currentImage="ghcr.io/acme/mqtt:1.4.1"
        catalogEntry={catalogEntry}
        allowAdhoc={false}
        onUpdate={onUpdate}
        onUpgrade={vi.fn()}
        onCancel={vi.fn()}
      />
    );

    expect(screen.getByText("1.4.2")).toBeDefined();
    fireEvent.click(screen.getByText("Update to 1.4.2"));
    expect(onUpdate).toHaveBeenCalledTimes(1);
  });

  it("hides the ad-hoc field when the capability is false", () => {
    render(
      <UpgradeDialog
        open
        connectorId="mqtt-01"
        currentImage="ghcr.io/acme/mqtt:1.4.1"
        catalogEntry={catalogEntry}
        allowAdhoc={false}
        onUpdate={vi.fn()}
        onUpgrade={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(screen.queryByLabelText("Ad-hoc image reference")).toBeNull();
  });

  it("shows the ad-hoc field when allowed, validates input, and fires onUpgrade with the trimmed ref", () => {
    const onUpgrade = vi.fn();
    render(
      <UpgradeDialog
        open
        connectorId="mqtt-01"
        currentImage={PINNED}
        catalogEntry={catalogEntry}
        allowAdhoc
        onUpdate={vi.fn()}
        onUpgrade={onUpgrade}
        onCancel={vi.fn()}
      />
    );

    const input = screen.getByLabelText("Ad-hoc image reference") as HTMLInputElement;
    expect(input).toBeDefined();

    // Default is the (pinned) current image → button enabled.
    const submit = screen.getByText("Upgrade to this image") as HTMLButtonElement;
    expect(submit.disabled).toBe(false);

    // Empty ref → invalid → disabled.
    fireEvent.change(input, { target: { value: "" } });
    expect((screen.getByText("Upgrade to this image") as HTMLButtonElement).disabled).toBe(true);

    // Valid ref again → enabled → fires with the value.
    fireEvent.change(input, { target: { value: `  ${PINNED}  ` } });
    fireEvent.click(screen.getByText("Upgrade to this image"));
    expect(onUpgrade).toHaveBeenCalledWith(PINNED);
  });

  it("notes when no catalog entry matches", () => {
    render(
      <UpgradeDialog
        open
        connectorId="mqtt-99"
        currentImage="ghcr.io/acme/mqtt:1.4.1"
        catalogEntry={undefined}
        allowAdhoc={false}
        onUpdate={vi.fn()}
        onUpgrade={vi.fn()}
        onCancel={vi.fn()}
      />
    );
    expect(screen.getByText(/No catalog entry matches/)).toBeDefined();
  });
});
