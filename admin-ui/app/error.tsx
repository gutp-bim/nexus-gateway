// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Route-level error boundary (#46). A thrown render error now shows a
// recoverable page — with reset() re-rendering the segment — instead of the
// white screen the app produced before (e.g. the Logs screen crashing on an
// unchecked payload).

export default function Error({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return (
    <div style={{ maxWidth: "32rem", margin: "3rem auto", textAlign: "center" }}>
      <h2 style={{ color: "#991b1b", marginBottom: "0.5rem" }}>Something went wrong</h2>
      <p style={{ color: "#6b7280", marginBottom: "1.5rem" }}>
        {error.message || "An unexpected error occurred while rendering this screen."}
      </p>
      <button
        onClick={reset}
        style={{
          border: "1px solid #2563eb",
          background: "#2563eb",
          color: "#fff",
          borderRadius: "0.375rem",
          padding: "0.5rem 1rem",
          cursor: "pointer",
        }}
      >
        Try again
      </button>
    </div>
  );
}
