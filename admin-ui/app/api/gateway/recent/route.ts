// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { getServerSession } from "next-auth";
import { authOptions } from "@/lib/auth";
import { NextResponse } from "next/server";

const BASE = process.env.ADMIN_API_URL ?? "http://localhost:8080";

export async function GET() {
  const session = await getServerSession(authOptions);
  if (!session) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  try {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (session.accessToken) headers["Authorization"] = `Bearer ${session.accessToken}`;
    const res = await fetch(`${BASE}/recent`, { headers });
    if (!res.ok) throw new Error(`Admin API /recent: ${res.status} ${res.statusText}`);
    return NextResponse.json(await res.json());
  } catch (err) {
    return NextResponse.json({ error: String(err) }, { status: 502 });
  }
}
