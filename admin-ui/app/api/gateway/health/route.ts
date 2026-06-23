// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getServerSession } from "next-auth";
import { authOptions } from "@/lib/auth";
import { getHealth } from "@/lib/api";
import { NextResponse } from "next/server";

export async function GET() {
  const session = await getServerSession(authOptions);
  if (!session?.accessToken) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  try {
    const health = await getHealth(session.accessToken);
    return NextResponse.json(health);
  } catch (err) {
    return NextResponse.json({ error: String(err) }, { status: 502 });
  }
}
