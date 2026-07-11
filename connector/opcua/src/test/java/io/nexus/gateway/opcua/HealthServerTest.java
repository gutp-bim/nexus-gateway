// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.util.concurrent.atomic.AtomicBoolean;

import static org.junit.jupiter.api.Assertions.*;

class HealthServerTest {

    private HealthServer server;

    @AfterEach
    void teardown() {
        if (server != null) {
            server.stop();
            server = null;
        }
    }

    private static HttpResponse<String> get(int port) throws Exception {
        HttpClient client = HttpClient.newHttpClient();
        HttpRequest req = HttpRequest.newBuilder()
            .uri(URI.create("http://127.0.0.1:" + port + "/health"))
            .GET()
            .build();
        return client.send(req, HttpResponse.BodyHandlers.ofString());
    }

    @Test
    void reportsDegradedWhenNotReady() throws Exception {
        AtomicBoolean ready = new AtomicBoolean(false);
        server = new HealthServer(0, ready::get);
        server.start();

        HttpResponse<String> resp = get(server.port());
        assertEquals(503, resp.statusCode());
        assertFalse(resp.body().contains("\"status\":\"ok\""),
            "degraded body must not contain the ok marker; got " + resp.body());
        assertTrue(resp.body().contains("degraded"), "got " + resp.body());
    }

    @Test
    void reportsOkWhenReady() throws Exception {
        AtomicBoolean ready = new AtomicBoolean(true);
        server = new HealthServer(0, ready::get);
        server.start();

        HttpResponse<String> resp = get(server.port());
        assertEquals(200, resp.statusCode());
        assertEquals("{\"status\":\"ok\"}", resp.body());
        assertEquals("application/json", resp.headers().firstValue("Content-Type").orElse(""));
    }

    @Test
    void readinessIsReEvaluatedPerRequest() throws Exception {
        AtomicBoolean ready = new AtomicBoolean(false);
        server = new HealthServer(0, ready::get);
        server.start();

        assertEquals(503, get(server.port()).statusCode());
        ready.set(true);
        assertEquals(200, get(server.port()).statusCode());
        ready.set(false);
        assertEquals(503, get(server.port()).statusCode());
    }
}
