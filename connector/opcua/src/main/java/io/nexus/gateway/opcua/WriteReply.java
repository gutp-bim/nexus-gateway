// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import com.fasterxml.jackson.databind.ObjectMapper;

public record WriteReply(boolean success, String response) {
    private static final ObjectMapper MAPPER = new ObjectMapper();

    public byte[] encode() throws Exception {
        return MAPPER.writeValueAsBytes(this);
    }
}
