// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import com.fasterxml.jackson.annotation.JsonProperty;

import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;

public record CommonEvent(
    @JsonProperty("protocol")     String protocol,
    @JsonProperty("connector_id") String connectorId,
    @JsonProperty("local_id")     String localId,
    @JsonProperty("device_ref")   String deviceRef,
    @JsonProperty("value")        double value,
    @JsonProperty("unit")         String unit,
    @JsonProperty("quality")      String quality,
    @JsonProperty("timestamp")    String timestamp
) {
    private static final DateTimeFormatter FMT =
        DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss'Z'").withZone(ZoneOffset.UTC);

    public static CommonEvent now(
        String connectorId, String localId, String deviceRef,
        double value, String unit, String quality
    ) {
        return new CommonEvent(
            "opcua", connectorId, localId, deviceRef,
            value, unit, quality, FMT.format(Instant.now())
        );
    }
}
