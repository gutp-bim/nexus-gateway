// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

/** Thin wrapper for Admin API calls forwarded from Next.js server routes. */

const BASE = process.env.ADMIN_API_URL ?? "http://localhost:8080";

export type GatewayHealth = {
  UptimeSeconds: number;
  GoRoutines: number;
  MemAllocMB: number;
  DiskUsedMB: number;
  DiskTotalMB: number;
  Connectors: ConnectorHealth[] | null;
};

export type ConnectorHealth = {
  ID: string;
  Image: string;
  Running: boolean;
};

export type ConnectorItem = {
  id: string;
  image: string;
  prev_image?: string;
  container_id?: string;
  running: boolean;
};

export type CatalogEntry = {
  name: string;
  version: string;
  image: string;
  digest: string;
  min_gateway_version: string;
  signature_required: boolean;
  network?: string[];
  mounts?: string[];
};

/** Thrown by adminFetch on a non-2xx response, carrying the real status code
 * so callers (route-helpers.ts) can pass 401/403 through instead of
 * collapsing every failure into a generic 502. */
export class AdminApiError extends Error {
  constructor(
    public status: number,
    public statusText: string,
    path: string,
    public body?: string
  ) {
    super(`Admin API ${path}: ${status} ${statusText}`);
    this.name = "AdminApiError";
  }
}

async function adminFetch(path: string, token: string | undefined, init?: RequestInit) {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(`${BASE}${path}`, { ...init, headers });
  if (!res.ok) {
    // The Go Admin API's auth middleware writes plain-text bodies
    // (http.Error), so capture with text(), not json().
    const body = await res.text().catch(() => undefined);
    throw new AdminApiError(res.status, res.statusText, path, body);
  }
  return res;
}

export async function getHealth(token?: string): Promise<GatewayHealth> {
  const res = await adminFetch("/health", token);
  return res.json();
}

export async function listConnectors(token?: string): Promise<ConnectorItem[]> {
  const res = await adminFetch("/connectors", token);
  return res.json();
}

export async function listCatalog(token?: string): Promise<CatalogEntry[]> {
  const res = await adminFetch("/catalog", token);
  return res.json();
}

export type PointEntry = {
  connector_id: string;
  protocol: string;
  local_id: string;
  point_id: string;
  unit?: string;
  writable?: boolean;
  device_ref?: string;
};

export type TelemetryStats = {
  buffer_depth: number;
  drifts: Record<string, number>;
};

export type ConnectorLogs = {
  connector_id: string;
  lines: string[];
};

export async function listDevices(token?: string): Promise<PointEntry[]> {
  const res = await adminFetch("/devices", token);
  return res.json();
}

export async function getTelemetry(token?: string): Promise<TelemetryStats> {
  const res = await adminFetch("/telemetry", token);
  return res.json();
}

export async function getConnectorLogs(token: string | undefined, id: string, tail = 100): Promise<ConnectorLogs> {
  const res = await adminFetch(`/logs/${encodeURIComponent(id)}?tail=${tail}`, token);
  return res.json();
}


export async function connectorAction(
  token: string | undefined,
  id: string,
  action: string,
  image?: string
): Promise<void> {
  const url = image
    ? `/connectors/${encodeURIComponent(id)}/${encodeURIComponent(action)}?image=${encodeURIComponent(image)}`
    : `/connectors/${encodeURIComponent(id)}/${encodeURIComponent(action)}`;
  await adminFetch(url, token, { method: "POST" });
}
