// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { usePolling } from "./use-polling";

describe("usePolling", () => {
  it("sets data + lastUpdated on success and is not stale", async () => {
    const fetcher = vi.fn().mockResolvedValue("v1");
    const { result } = renderHook(() => usePolling(fetcher, { intervalMs: 10_000 }));

    await waitFor(() => expect(result.current.data).toBe("v1"));
    expect(result.current.lastUpdated).not.toBeNull();
    expect(result.current.stale).toBe(false);
    expect(result.current.error).toBeNull();
    expect(result.current.loading).toBe(false);
  });

  it("keeps prior data and flags stale when a later poll fails", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce("v1")
      .mockRejectedValue(new Error("boom"));
    const { result } = renderHook(() => usePolling(fetcher, { intervalMs: 10_000 }));

    await waitFor(() => expect(result.current.data).toBe("v1"));
    await act(async () => {
      await result.current.refresh();
    });

    expect(result.current.data).toBe("v1"); // prior data retained, not blanked
    expect(result.current.stale).toBe(true);
    expect(result.current.error).toBeInstanceOf(Error);
  });

  it("does not poll while disabled (pause)", async () => {
    const fetcher = vi.fn().mockResolvedValue("v");
    renderHook(() => usePolling(fetcher, { intervalMs: 10, enabled: false }));
    await new Promise((r) => setTimeout(r, 50));
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("polls on an interval while enabled and stops when it flips to disabled", async () => {
    const fetcher = vi.fn().mockResolvedValue("v");
    const { rerender } = renderHook(
      ({ enabled }) => usePolling(fetcher, { intervalMs: 20, enabled }),
      { initialProps: { enabled: true } }
    );

    await waitFor(() => expect(fetcher.mock.calls.length).toBeGreaterThanOrEqual(2));
    const atPause = fetcher.mock.calls.length;

    rerender({ enabled: false });
    await new Promise((r) => setTimeout(r, 80));
    // At most one already-scheduled tick may land; no continued growth.
    expect(fetcher.mock.calls.length).toBeLessThanOrEqual(atPause + 1);
  });

  it("skips overlapping polls (reentrancy guard)", async () => {
    let resolve!: (v: string) => void;
    const fetcher = vi.fn().mockImplementation(
      () =>
        new Promise<string>((r) => {
          resolve = r;
        })
    );
    const { result } = renderHook(() => usePolling(fetcher, { intervalMs: 10_000 }));

    // First run is in-flight; a manual refresh must not start a second fetch.
    await act(async () => {
      result.current.refresh();
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(result.current.fetching).toBe(true); // a fetch is in flight
    await act(async () => {
      resolve("done");
    });
    await waitFor(() => expect(result.current.data).toBe("done"));
    expect(result.current.fetching).toBe(false); // settled
  });
});
