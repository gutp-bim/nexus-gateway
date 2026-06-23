// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.databind.ObjectMapper;

public record ControlCommand(
    @JsonProperty("control_id") String controlId,
    @JsonProperty("local_id")   String localId,
    @JsonProperty("device_ref") String deviceRef,
    double value,
    int priority
) {
    private static final ObjectMapper MAPPER = new ObjectMapper();

    public static ControlCommand fromBytes(byte[] data) throws Exception {
        return MAPPER.readValue(data, ControlCommand.class);
    }
}
