// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getServerSession } from "next-auth";
import { authOptions } from "@/lib/auth";
import { listConnectors } from "@/lib/api";
import { NextResponse } from "next/server";

export async function GET() {
  const session = await getServerSession(authOptions);
  if (!session) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  try {
    const connectors = await listConnectors(session.accessToken);
    return NextResponse.json(connectors);
  } catch (err) {
    return NextResponse.json({ error: String(err) }, { status: 502 });
  }
}
