// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getServerSession } from "next-auth";
import { authOptions } from "@/lib/auth";
import { getConnectorLogs } from "@/lib/api";
import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const session = await getServerSession(authOptions);
  if (!session?.accessToken) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  const { id } = await params;
  const tail = Number(req.nextUrl.searchParams.get("tail") ?? "100");
  try {
    const logs = await getConnectorLogs(session.accessToken, id, tail);
    return NextResponse.json(logs);
  } catch (err) {
    return NextResponse.json({ error: String(err) }, { status: 502 });
  }
}
