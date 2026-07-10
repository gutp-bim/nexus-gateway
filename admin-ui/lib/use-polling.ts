// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

// Shared polling hook (#41). Every screen used to hand-roll its own
// useEffect + setInterval, and only the dashboard tracked a last-updated time;
// a failed poll silently froze the others. This centralizes: a reentrancy
// guard (generalizing the fetchingRef in devices/telemetry), a last-success
// timestamp, and a `stale` flag so a screen can keep showing prior data with a
// badge instead of blanking on a transient poll failure.

import { useCallback, useEffect, useRef, useState } from "react";

export type PollingState<T> = {
  /** Latest successful result, or undefined before the first success. */
  data: T | undefined;
  /** The last poll's error (null when the last poll succeeded). */
  error: unknown;
  /** True until the first poll settles (success or failure) — for a first-load spinner. */
  loading: boolean;
  /** True while any poll (initial, interval, or manual refresh) is in flight — for a busy/disabled control. */
  fetching: boolean;
  /** When `data` was last refreshed successfully. */
  lastUpdated: Date | null;
  /** The last poll failed but prior `data` is still shown. */
  stale: boolean;
  /** Trigger a poll now (also used for manual refresh / on-select fetches). */
  refresh: () => void;
};

export function usePolling<T>(
  fetcher: () => Promise<T>,
  options: { intervalMs: number; enabled?: boolean }
): PollingState<T> {
  const { intervalMs, enabled = true } = options;
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const [fetching, setFetching] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [stale, setStale] = useState(false);

  const inFlight = useRef(false);
  // Always call the latest fetcher (which may close over changing state like a
  // selected id) without making it an effect dependency that restarts polling.
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const run = useCallback(async () => {
    if (inFlight.current) return; // skip overlapping polls
    inFlight.current = true;
    setFetching(true);
    try {
      const result = await fetcherRef.current();
      setData(result);
      setError(null);
      setStale(false);
      setLastUpdated(new Date());
    } catch (e) {
      // Keep prior data; mark it stale rather than throwing the screen away.
      setError(e);
      setStale(true);
    } finally {
      setLoading(false);
      setFetching(false);
      inFlight.current = false;
    }
  }, []);

  useEffect(() => {
    if (!enabled) return;
    run();
    const id = setInterval(run, intervalMs);
    return () => clearInterval(id);
  }, [run, intervalMs, enabled]);

  return { data, error, loading, fetching, lastUpdated, stale, refresh: run };
}
