// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

public record PointConfig(
    String localId,      // OPC-UA NodeId string, e.g. "ns=2;i=1001"
    String deviceRef,    // opaque device reference for the Normalizer
    String unit,         // engineering unit, e.g. "degC"
    boolean writable,
    String methodNodeId  // null = direct Write, non-null = Method Call on this NodeId
) {
    /** Backward-compatible constructor for read-only points. */
    public PointConfig(String localId, String deviceRef, String unit) {
        this(localId, deviceRef, unit, false, null);
    }
}
