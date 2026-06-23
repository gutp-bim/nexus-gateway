// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.Map;
import java.util.concurrent.*;
import java.util.stream.Collectors;

/**
 * Handles write commands on cmd.opcua.<connector_id> (ADR-0004).
 * Thread-safe: NATS dispatcher may deliver messages on multiple threads.
 */
public class WriteHandler {

    @FunctionalInterface
    public interface Responder {
        void reply(String replyTo, byte[] data) throws Exception;
    }

    private static final int DEDUP_MAX = 1000;
    private static final String IN_FLIGHT = "__in_flight__";
    private static final Logger log = LoggerFactory.getLogger(WriteHandler.class);

    private final Config cfg;
    private final OpcUaClientFacade client;
    private final Responder responder;
    private final Map<String, PointConfig> pointMap;
    private final ConcurrentHashMap<String, WriteReply> dedup = new ConcurrentHashMap<>();
    private final ExecutorService writeExecutor = Executors.newCachedThreadPool(r -> {
        Thread t = new Thread(r, "opcua-write");
        t.setDaemon(true);
        return t;
    });

    public WriteHandler(Config cfg, OpcUaClientFacade client, Responder responder) {
        this.cfg = cfg;
        this.client = client;
        this.responder = responder;
        this.pointMap = cfg.points().stream()
            .collect(Collectors.toMap(PointConfig::localId, p -> p));
    }

    public void handle(byte[] data, String replyTo) {
        ControlCommand cmd;
        try {
            cmd = ControlCommand.fromBytes(data);
        } catch (Exception ex) {
            log.warn("opcua: unparseable command: {}", ex.getMessage());
            send(replyTo, new WriteReply(false, "bad_request"));
            return;
        }

        // Idempotency: putIfAbsent with sentinel prevents concurrent double-writes (TOCTOU-safe).
        WriteReply sentinel = new WriteReply(false, IN_FLIGHT);
        WriteReply previous = dedup.putIfAbsent(cmd.controlId(), sentinel);
        if (previous != null) {
            if (!IN_FLIGHT.equals(previous.response())) {
                send(replyTo, previous);
            }
            // else: in-flight duplicate — drop silently; retry will arrive after result is stored
            return;
        }

        WriteReply result = execute(cmd);
        dedup.put(cmd.controlId(), result);
        evictIfNeeded();
        send(replyTo, result);
    }

    private WriteReply execute(ControlCommand cmd) {
        PointConfig pt = pointMap.get(cmd.localId());
        if (pt == null || !pt.writable()) {
            return new WriteReply(false, "not_writable");
        }

        long timeoutMs = (long) (cfg.writeTimeout() * 1000);
        Future<Void> future;
        try {
            future = writeExecutor.submit(() -> {
                if (pt.methodNodeId() != null) {
                    client.callMethod(cmd.localId(), pt.methodNodeId(), cmd.value());
                } else {
                    client.writeNode(cmd.localId(), cmd.value());
                }
                return null;
            });
        } catch (RejectedExecutionException ex) {
            log.warn("opcua: write executor unavailable for {} control_id={}", cmd.localId(), cmd.controlId());
            return new WriteReply(false, "device_error: executor_shutdown");
        }

        try {
            future.get(timeoutMs, TimeUnit.MILLISECONDS);
            log.info("opcua: wrote {}={} control_id={}", cmd.localId(), cmd.value(), cmd.controlId());
            return new WriteReply(true, "ok");
        } catch (TimeoutException ex) {
            future.cancel(true);
            log.warn("opcua: write timed out for {} control_id={}", cmd.localId(), cmd.controlId());
            return new WriteReply(false, "timeout");
        } catch (ExecutionException ex) {
            Throwable cause = ex.getCause() != null ? ex.getCause() : ex;
            log.warn("opcua: write device error for {}: {}", cmd.localId(), cause.getMessage());
            return new WriteReply(false, "device_error: " + cause.getMessage());
        } catch (InterruptedException ex) {
            Thread.currentThread().interrupt();
            return new WriteReply(false, "interrupted");
        }
    }

    private void evictIfNeeded() {
        // Skip IN_FLIGHT sentinels — evicting them breaks the TOCTOU-safe idempotency guarantee.
        while (dedup.size() > DEDUP_MAX) {
            boolean removed = false;
            for (Map.Entry<String, WriteReply> entry : dedup.entrySet()) {
                if (!IN_FLIGHT.equals(entry.getValue().response())) {
                    if (dedup.remove(entry.getKey(), entry.getValue())) {
                        removed = true;
                        break;
                    }
                }
            }
            if (!removed) break; // only sentinels remain; don't evict
        }
    }

    private void send(String replyTo, WriteReply reply) {
        if (replyTo == null || replyTo.isBlank()) return;
        try {
            responder.reply(replyTo, reply.encode());
        } catch (Exception ex) {
            log.warn("opcua: failed to send write reply: {}", ex.getMessage());
        }
    }
}
