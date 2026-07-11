// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.function.BooleanSupplier;

/**
 * Minimal health endpoint backed by the JDK's built-in {@link HttpServer} (no extra dependency).
 *
 * <p>{@code GET /health} returns {@code 200 {"status":"ok"}} when the supplied readiness predicate
 * is true, otherwise {@code 503 {"status":"degraded"}}. Binding failures are logged and treated as
 * non-fatal so the connector keeps running even if the port is unavailable.
 */
public class HealthServer {

    private static final Logger log = LoggerFactory.getLogger(HealthServer.class);

    private static final byte[] OK_BODY = "{\"status\":\"ok\"}".getBytes(StandardCharsets.UTF_8);
    private static final byte[] DEGRADED_BODY = "{\"status\":\"degraded\"}".getBytes(StandardCharsets.UTF_8);

    private final int requestedPort;
    private final BooleanSupplier ready;

    private HttpServer server;
    private ExecutorService executor;

    public HealthServer(int port, BooleanSupplier ready) {
        this.requestedPort = port;
        this.ready = ready;
    }

    /** Start listening. Binding failures are logged and swallowed (non-fatal). */
    public void start() {
        try {
            server = HttpServer.create(new InetSocketAddress(requestedPort), 0);
            executor = Executors.newFixedThreadPool(2, r -> {
                Thread t = new Thread(r, "opcua-health");
                t.setDaemon(true);
                return t;
            });
            server.setExecutor(executor);
            server.createContext("/health", this::handleHealth);
            server.start();
            log.info("opcua: health server listening on port {}", port());
        } catch (IOException e) {
            log.warn("opcua: health server failed to bind on port {} — continuing without it: {}",
                requestedPort, e.getMessage());
            server = null;
            if (executor != null) {
                executor.shutdownNow();
                executor = null;
            }
        }
    }

    /** Stop the server if it is running. Safe to call when start failed. */
    public void stop() {
        if (server != null) {
            server.stop(0);
            server = null;
        }
        if (executor != null) {
            executor.shutdownNow();
            executor = null;
        }
    }

    /** The actual bound port (useful when constructed with port 0), or the requested port if not started. */
    public int port() {
        return server != null ? server.getAddress().getPort() : requestedPort;
    }

    private void handleHealth(HttpExchange exchange) throws IOException {
        boolean ok;
        try {
            ok = ready.getAsBoolean();
        } catch (RuntimeException e) {
            ok = false;
        }
        byte[] body = ok ? OK_BODY : DEGRADED_BODY;
        int status = ok ? 200 : 503;
        exchange.getResponseHeaders().set("Content-Type", "application/json");
        exchange.sendResponseHeaders(status, body.length);
        try (OutputStream os = exchange.getResponseBody()) {
            os.write(body);
        }
    }
}
