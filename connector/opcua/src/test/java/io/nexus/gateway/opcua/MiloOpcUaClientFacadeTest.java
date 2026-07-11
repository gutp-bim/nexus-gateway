// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class MiloOpcUaClientFacadeTest {

    @Test
    void isConnectedFalseBeforeConnect() {
        MiloOpcUaClientFacade facade = new MiloOpcUaClientFacade("opc.tcp://localhost:4840");
        assertFalse(facade.isConnected(), "facade must report not connected before connect()");
    }

    @Test
    void healthWiringReflectsFacadeReadiness() throws Exception {
        // The connector wires HealthServer to facade::isConnected; verify the supplier plumbs through.
        MiloOpcUaClientFacade facade = new MiloOpcUaClientFacade("opc.tcp://localhost:4840");
        HealthServer server = new HealthServer(0, facade::isConnected);
        try {
            server.start();
            var client = java.net.http.HttpClient.newHttpClient();
            var req = java.net.http.HttpRequest.newBuilder()
                .uri(java.net.URI.create("http://127.0.0.1:" + server.port() + "/health"))
                .GET().build();
            var resp = client.send(req, java.net.http.HttpResponse.BodyHandlers.ofString());
            // Not connected → degraded.
            assertEquals(503, resp.statusCode());
            assertFalse(resp.body().contains("\"status\":\"ok\""));
        } finally {
            server.stop();
        }
    }
}
