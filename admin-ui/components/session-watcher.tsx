// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useSession } from "next-auth/react";

// Mounted globally (see Providers). Redirects to the custom sign-in page the
// moment a persisted refresh failure is observed — including on
// background/polling screens that never call useSession() themselves, since
// SessionProvider's refetchInterval (see Providers) keeps this component's
// own session view current. Immediate/unconditional redirect, not a
// dismissible banner: a refresh failure only happens when the refresh token
// itself is rejected (revoked session, Keycloak restart), a state with no
// useful degraded UI to preserve.
export function SessionWatcher() {
  const { data: session } = useSession();
  const router = useRouter();

  useEffect(() => {
    if (session?.error === "RefreshAccessTokenError") {
      router.push(`/auth/signin?reason=expired&callbackUrl=${encodeURIComponent(window.location.href)}`);
    }
  }, [session?.error, router]);

  return null;
}
