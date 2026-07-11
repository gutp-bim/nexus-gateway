// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import io.nats.client.Options;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class MainTest {

    @Test
    void natsOptionsConfiguresUnlimitedReconnect() {
        // jnats defaults to ~60 reconnect attempts; the connector must reconnect
        // indefinitely so an outage longer than ~2 minutes does not leave it silent (#30).
        Options opts = Main.natsOptions("nats://localhost:4222");
        assertEquals(-1, opts.getMaxReconnect(), "maxReconnects must be -1 (unlimited)");
        assertTrue(opts.getReconnectWait().toMillis() > 0, "reconnect backoff must be positive");
    }
}
