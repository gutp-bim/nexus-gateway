// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

"use client";

import { useCallback, useEffect, useState } from "react";
import { useSession } from "next-auth/react";
import { ConnectorTable } from "@/components/connector-table";
import type { ConnectorItem } from "@/lib/api";

export default function ConnectorsPage() {
  const { data: session } = useSession();
  const [connectors, setConnectors] = useState<ConnectorItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const fetchConnectors = useCallback(async () => {
    try {
      const res = await fetch("/api/gateway/connectors");
      if (!res.ok) throw new Error(`${res.status}`);
      setConnectors(await res.json());
      setError(null);
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchConnectors();
    const id = setInterval(fetchConnectors, 10_000);
    return () => clearInterval(id);
  }, [fetchConnectors]);

  const isOperator = session?.realmRoles?.includes("gateway-operator") ?? false;

  return (
    <div>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: "1.25rem" }}>
        <h1 style={{ fontSize: "1.25rem", fontWeight: 700, margin: 0 }}>Connectors</h1>
        {!isOperator && (
          <span style={{ fontSize: "0.8rem", color: "#6b7280", background: "#f3f4f6", padding: "0.2rem 0.6rem", borderRadius: "999px" }}>
            viewer — actions disabled
          </span>
        )}
      </div>
      {loading && <p>Loading…</p>}
      {error && <p style={{ color: "#dc2626" }}>Failed to load connectors: {error}</p>}
      {!loading && (
        <ConnectorTable data={connectors} isOperator={isOperator} onRefresh={fetchConnectors} />
      )}
    </div>
  );
}
