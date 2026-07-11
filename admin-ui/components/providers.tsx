// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { SessionProvider } from "next-auth/react";
import { SessionWatcher } from "@/components/session-watcher";
import { ToastProvider } from "@/components/toast";
import { LocaleProvider } from "@/components/locale-provider";

export function Providers({ children }: { children: React.ReactNode }) {
  // Periodic client-side session refetch, well under a typical Keycloak
  // access-token TTL of a few minutes — this is what keeps SessionWatcher's
  // view of session.error timely for screens that poll their own data but
  // never call useSession() themselves (dashboard, telemetry, devices, logs).
  //
  // LocaleProvider is outermost so every consumer (Nav, toasts, pages) can
  // resolve translations (#43).
  return (
    <LocaleProvider>
      <SessionProvider refetchInterval={60}>
        <SessionWatcher />
        <ToastProvider>{children}</ToastProvider>
      </SessionProvider>
    </LocaleProvider>
  );
}
