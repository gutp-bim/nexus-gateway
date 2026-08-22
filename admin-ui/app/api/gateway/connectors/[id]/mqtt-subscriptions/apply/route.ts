// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getServerSession } from "next-auth";
import { authOptions } from "@/lib/auth";
import { applyMqttSubscriptions } from "@/lib/api";
import { withAdminApi } from "@/lib/route-helpers";

export async function POST(_req: Request, { params }: { params: Promise<{ id: string }> }) {
  const session = await getServerSession(authOptions);
  const { id } = await params;
  return withAdminApi(session, () => applyMqttSubscriptions(session?.accessToken, id));
}
