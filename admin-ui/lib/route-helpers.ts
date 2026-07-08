// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { NextResponse } from "next/server";
import type { Session } from "next-auth";
import { AdminApiError } from "./api";

/**
 * Returns a 401 response if there's no session or the session is errored
 * (refresh failed — see lib/auth.ts), else null to let the caller proceed.
 * Session is passed in (not resolved internally) so this stays unit-testable
 * without mocking next-auth.
 */
export function sessionGuard(session: Session | null): NextResponse | null {
  if (!session || session.error) {
    return NextResponse.json({ error: "session_expired" }, { status: 401 });
  }
  return null;
}

/**
 * Runs `work()` after checking sessionGuard, mapping the result/error to a
 * Response:
 *  - undefined result     -> 204 No Content (connector actions)
 *  - any other result     -> 200 JSON
 *  - AdminApiError(401|403) -> passed through as-is (previously flattened to 502)
 *  - any other error      -> 502 Bad Gateway
 */
export async function withAdminApi<T>(
  session: Session | null,
  work: () => Promise<T>
): Promise<NextResponse> {
  const guard = sessionGuard(session);
  if (guard) return guard;
  try {
    const result = await work();
    return result === undefined
      ? new NextResponse(null, { status: 204 })
      : NextResponse.json(result);
  } catch (err) {
    if (err instanceof AdminApiError) {
      const status = err.status === 401 || err.status === 403 ? err.status : 502;
      return NextResponse.json({ error: err.message }, { status });
    }
    return NextResponse.json({ error: String(err) }, { status: 502 });
  }
}
