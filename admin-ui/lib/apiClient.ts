// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

/** Shared client-side fetch wrapper for all screens (issue #39). */

export type ApiErrorKind =
  | "unauthorized" // 401 — triggers sign-in
  | "forbidden" // 403 — distinct "no permission" message, NEVER redirects (RBAC role mismatch isn't fixed by re-auth)
  | "not_found" // 404
  | "bad_gateway" // 502/503/504
  | "server_error" // 500 and any other unmapped status
  | "network" // fetch itself threw
  | "invalid_response"; // ok, but JSON parse or shape guard failed

export class ApiError extends Error {
  constructor(
    public kind: ApiErrorKind,
    message: string,
    public status?: number
  ) {
    super(message);
    this.name = "ApiError";
  }
}

const MESSAGES: Record<ApiErrorKind, string> = {
  unauthorized: "Your session has expired. Redirecting to sign-in…",
  forbidden: "You don't have permission to perform this action.",
  not_found: "Not found.",
  bad_gateway: "Gateway is unreachable. Try again shortly.",
  server_error: "Something went wrong on the server. Try again shortly.",
  network: "Could not reach the Admin UI server. Check your connection.",
  invalid_response: "Received an unexpected response. Try again.",
};

function classify(status: number): ApiErrorKind {
  if (status === 401) return "unauthorized";
  if (status === 403) return "forbidden";
  if (status === 404) return "not_found";
  if (status === 502 || status === 503 || status === 504) return "bad_gateway";
  return "server_error";
}

// Module-level dedupe guard: several apiFetch calls can 401 around the same
// time (e.g. a screen firing two GETs in parallel) — only trigger sign-in once.
// NOTE: this path calls next-auth's signIn() directly, so — unlike
// SessionWatcher's redirect to /auth/signin?reason=expired — it doesn't carry
// the "your session expired" explanation (signIn()'s API only exposes
// `callbackUrl`, not arbitrary extra query params on the redirect). Known,
// accepted gap: the user still lands on the sign-in page, just without that
// one banner on this specific path.
let signingIn = false;
function triggerSignIn() {
  if (signingIn) return;
  signingIn = true;
  import("next-auth/react")
    .then(({ signIn }) => signIn(undefined, { callbackUrl: window.location.href }))
    .catch(() => {
      // A failed redirect (chunk-load error, offline) must not leave 401
      // handling permanently disabled for the rest of the tab's life.
      signingIn = false;
    });
}

/** Type guard builder: array of T, optionally validating each element with itemGuard. */
export function isArrayOf<T>(itemGuard?: (x: unknown) => x is T) {
  return (data: unknown): data is T[] => Array.isArray(data) && (!itemGuard || data.every(itemGuard));
}

/** Type guard: a plain (non-array) object. Generic so it can be used directly
 * as an apiFetch guard for a specific response shape (T inferred from the
 * call site's expected type), not just the bare Record<string, unknown>. */
export function isRecord<T extends Record<string, unknown> = Record<string, unknown>>(
  data: unknown
): data is T {
  return typeof data === "object" && data !== null && !Array.isArray(data);
}

/**
 * Fetches `path`, classifying any failure into an ApiError with a
 * human-readable message. On 401, centrally triggers sign-in (callers do not
 * need to handle this themselves). 204 resolves to `undefined`. When `guard`
 * is supplied, a response that parses but doesn't match the expected shape
 * throws `invalid_response` instead of letting a malformed payload reach the
 * caller (e.g. `.map()` on a non-array).
 */
export async function apiFetch<T>(
  path: string,
  init?: RequestInit,
  guard?: (data: unknown) => data is T
): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, init);
  } catch {
    throw new ApiError("network", MESSAGES.network);
  }

  if (!res.ok) {
    const kind = classify(res.status);
    if (kind === "unauthorized") triggerSignIn();
    throw new ApiError(kind, MESSAGES[kind], res.status);
  }

  if (res.status === 204) return undefined as T;

  let data: unknown;
  try {
    data = await res.json();
  } catch {
    throw new ApiError("invalid_response", MESSAGES.invalid_response);
  }

  if (guard && !guard(data)) {
    throw new ApiError("invalid_response", MESSAGES.invalid_response);
  }
  return data as T;
}
