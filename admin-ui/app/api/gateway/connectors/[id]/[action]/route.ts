// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getServerSession } from "next-auth";
import { authOptions } from "@/lib/auth";
import { connectorAction } from "@/lib/api";
import { withAdminApi } from "@/lib/route-helpers";
import { NextRequest } from "next/server";

export async function POST(
  req: NextRequest,
  { params }: { params: Promise<{ id: string; action: string }> }
) {
  const session = await getServerSession(authOptions);
  const { id, action } = await params;
  const image = req.nextUrl.searchParams.get("image") ?? undefined;
  return withAdminApi(session, () => connectorAction(session?.accessToken, id, action, image));
}
