// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getServerSession } from "next-auth";
import { authOptions } from "@/lib/auth";
import { getConnectorLogs } from "@/lib/api";
import { withAdminApi } from "@/lib/route-helpers";
import { NextRequest } from "next/server";

export async function GET(req: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const session = await getServerSession(authOptions);
  const { id } = await params;
  const tail = Number(req.nextUrl.searchParams.get("tail") ?? "100");
  return withAdminApi(session, () => getConnectorLogs(session?.accessToken, id, tail));
}
