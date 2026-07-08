// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getServerSession } from "next-auth";
import { authOptions } from "@/lib/auth";
import { listDevices } from "@/lib/api";
import { withAdminApi } from "@/lib/route-helpers";

export async function GET() {
  const session = await getServerSession(authOptions);
  return withAdminApi(session, () => listDevices(session?.accessToken));
}
