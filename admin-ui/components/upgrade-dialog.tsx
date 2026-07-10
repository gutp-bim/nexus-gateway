// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Upgrade dialog (#40), replacing the native window.prompt("New image reference:").
// The prompt gave no context and accepted any string. This dialog surfaces the
// connector's catalog entry (the vetted, digest-pinned target) as the primary
// path, and only exposes the free-form ad-hoc image field when the server
// advertises allow_adhoc_upgrade — otherwise the ad-hoc path is 501 anyway
// (ADR-0006: catalog-driven by default).

import { useState } from "react";
import type { CatalogEntry } from "@/lib/api";
import { Dialog, DialogButton } from "@/components/dialog";

type Props = {
  open: boolean;
  connectorId: string;
  currentImage: string;
  /** Catalog entry matching this connector (by name === id), if any. */
  catalogEntry?: CatalogEntry;
  /** Server capability: is the dev-only ad-hoc upgrade?image= path enabled? */
  allowAdhoc: boolean;
  /** Catalog-driven update (POST /connectors/{id}/update — server picks the target). */
  onUpdate: () => void;
  /** Ad-hoc upgrade to an explicit image reference (POST /connectors/{id}/upgrade?image=). */
  onUpgrade: (image: string) => void;
  onCancel: () => void;
};

/**
 * Loose validation of an OCI image reference for the ad-hoc field. This is a UI
 * guardrail, not a spec-complete parser — the server re-validates. `valid` gates
 * submission; `warning` is advisory (surfaced but non-blocking).
 */
export function validateImageRef(ref: string): { valid: boolean; warning?: string } {
  const trimmed = ref.trim();
  if (!trimmed) return { valid: false };
  if (/\s/.test(trimmed)) return { valid: false };
  if (!/^[a-zA-Z0-9][a-zA-Z0-9._\-:/@]*$/.test(trimmed)) return { valid: false };
  const digestPinned = /@sha256:[a-f0-9]{64}$/i.test(trimmed);
  if (!digestPinned) {
    return {
      valid: true,
      warning: "Not digest-pinned (@sha256:…). A tag can be re-pointed; pin a digest for a reproducible, verifiable deploy.",
    };
  }
  return { valid: true };
}

function shortDigest(d: string): string {
  if (!d) return "—";
  const hex = d.startsWith("sha256:") ? d.slice(7) : d.includes(":") ? d.slice(d.indexOf(":") + 1) : d;
  return hex.length >= 12 ? `${hex.slice(0, 12)}…` : hex || "—";
}

export function UpgradeDialog({
  open,
  connectorId,
  currentImage,
  catalogEntry,
  allowAdhoc,
  onUpdate,
  onUpgrade,
  onCancel,
}: Props) {
  const [adhocImage, setAdhocImage] = useState(currentImage);
  const check = validateImageRef(adhocImage);

  return (
    <Dialog open={open} title={`Upgrade ${connectorId}`} onClose={onCancel} titleId="upgrade-dialog-title">
      <div style={{ fontSize: "0.85rem", color: "#6b7280", marginBottom: "1rem", wordBreak: "break-all" }}>
        Current image: <span style={{ fontFamily: "monospace", color: "#374151" }}>{currentImage || "—"}</span>
      </div>

      {/* Primary path: catalog-driven update. */}
      <section style={{ marginBottom: allowAdhoc ? "1.25rem" : 0 }}>
        <h3 style={{ fontSize: "0.9rem", fontWeight: 700, margin: "0 0 0.5rem" }}>Catalog version</h3>
        {catalogEntry ? (
          <>
            <table style={{ fontSize: "0.85rem", width: "100%", borderCollapse: "collapse", marginBottom: "0.75rem" }}>
              <tbody>
                <Row label="Name" value={catalogEntry.name} />
                <Row label="Version" value={catalogEntry.version} />
                <Row label="Digest" value={<span title={catalogEntry.digest} style={{ fontFamily: "monospace" }}>{shortDigest(catalogEntry.digest)}</span>} />
              </tbody>
            </table>
            <DialogButton label={`Update to ${catalogEntry.version}`} onClick={onUpdate} variant="primary" />
          </>
        ) : (
          <p style={{ fontSize: "0.85rem", color: "#9ca3af", margin: 0 }}>
            No catalog entry matches this connector.
          </p>
        )}
      </section>

      {/* Escape hatch: ad-hoc image, only when the server allows it. */}
      {allowAdhoc && (
        <section style={{ borderTop: "1px solid #e5e7eb", paddingTop: "1rem" }}>
          <h3 style={{ fontSize: "0.9rem", fontWeight: 700, margin: "0 0 0.25rem" }}>Ad-hoc image (advanced)</h3>
          <p style={{ fontSize: "0.78rem", color: "#6b7280", margin: "0 0 0.5rem" }}>
            Dev-only override. Prefer the catalog version above for production.
          </p>
          <input
            type="text"
            aria-label="Ad-hoc image reference"
            value={adhocImage}
            onChange={(e) => setAdhocImage(e.target.value)}
            placeholder="registry/image@sha256:…"
            style={{
              width: "100%",
              padding: "0.4rem 0.6rem",
              fontSize: "0.85rem",
              fontFamily: "monospace",
              border: "1px solid #d1d5db",
              borderRadius: "0.3rem",
              boxSizing: "border-box",
            }}
          />
          {adhocImage.trim() !== "" && !check.valid && (
            <p role="alert" style={{ fontSize: "0.78rem", color: "#dc2626", margin: "0.4rem 0 0" }}>
              Not a valid image reference.
            </p>
          )}
          {check.valid && check.warning && (
            <p style={{ fontSize: "0.78rem", color: "#d97706", margin: "0.4rem 0 0" }}>⚠ {check.warning}</p>
          )}
          <div style={{ marginTop: "0.6rem" }}>
            <DialogButton
              label="Upgrade to this image"
              onClick={() => onUpgrade(adhocImage.trim())}
              variant="danger"
              disabled={!check.valid}
            />
          </div>
        </section>
      )}

      <div style={{ display: "flex", justifyContent: "flex-end", marginTop: "1.25rem" }}>
        <DialogButton label="Cancel" onClick={onCancel} variant="default" />
      </div>
    </Dialog>
  );
}

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <tr>
      <td style={{ padding: "0.2rem 0.5rem 0.2rem 0", color: "#6b7280", whiteSpace: "nowrap", verticalAlign: "top" }}>{label}</td>
      <td style={{ padding: "0.2rem 0", color: "#111", wordBreak: "break-all" }}>{value}</td>
    </tr>
  );
}
