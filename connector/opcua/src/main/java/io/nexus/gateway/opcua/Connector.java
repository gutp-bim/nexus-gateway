// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.stream.Collectors;

/** Connects to an OPC-UA server and publishes Common Events via the injected publisher. */
public class Connector {

    @FunctionalInterface
    public interface Publisher {
        void publish(String subject, byte[] data) throws Exception;
    }

    private static final Logger log = LoggerFactory.getLogger(Connector.class);
    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final Config cfg;
    private final OpcUaClientFacade client;
    private final Publisher publisher;
    private final Map<String, PointConfig> pointMap;
    private final List<String> nodeIds;
    private final String subject;
    private final CountDownLatch stopLatch = new CountDownLatch(1);

    public Connector(Config cfg, OpcUaClientFacade client, Publisher publisher) {
        this.cfg = cfg;
        this.client = client;
        this.publisher = publisher;
        this.pointMap = cfg.points().stream()
            .collect(Collectors.toMap(PointConfig::localId, p -> p));
        this.nodeIds = cfg.points().stream().map(PointConfig::localId).toList();
        this.subject = "evt.opcua." + cfg.connectorId();
    }

    /**
     * Connect, poll once, subscribe, then re-poll every {@code pollInterval}
     * seconds until stop() is called. The periodic re-poll is a freshness floor
     * alongside the change-driven subscription (#110): with static server values
     * the subscription fires nothing, so polling guarantees every point is
     * refreshed at least once per interval.
     */
    public void run() throws Exception {
        log.info("opcua: connector {} starting, endpoint={}", cfg.connectorId(), cfg.opcuaEndpoint());
        client.connect();

        // Log browse results for Point List authoring (AC: browse logged locally)
        try {
            Map<String, String> nodes = client.browse("i=85"); // Objects folder
            log.info("opcua: browse found {} nodes under Objects", nodes.size());
            nodes.forEach((id, name) -> log.info("  {} => {}", id, name));
        } catch (Exception ex) {
            log.warn("opcua: browse failed (non-fatal): {}", ex.getMessage());
        }

        try {
            // Initial poll
            pollAll();

            // Subscribe to monitored items for all configured points
            if (!nodeIds.isEmpty()) {
                client.subscribe(nodeIds, this::onValue);
            }

            log.info("opcua: connector {} subscribed to {} points, re-poll every {}s",
                cfg.connectorId(), nodeIds.size(), cfg.pollInterval());

            // Periodic re-poll backstop alongside the subscription (#110).
            // await() returns true once stop() fires; false on timeout (interval
            // elapsed) → re-poll. At least 1 ms to avoid a busy loop.
            long intervalMs = Math.max(1L, (long) (cfg.pollInterval() * 1000));
            while (!stopLatch.await(intervalMs, TimeUnit.MILLISECONDS)) {
                pollAll();
            }
        } finally {
            client.close();
            log.info("opcua: connector {} stopped", cfg.connectorId());
        }
    }

    public void stop() {
        stopLatch.countDown();
    }

    private void onValue(String nodeId, OpcValue opcValue) {
        PointConfig pt = pointMap.get(nodeId);
        if (pt == null) return;
        Double value = opcValue.toDouble();
        if (value == null) {
            log.debug("opcua: skipping non-numeric value for {}", nodeId);
            return;
        }
        publish(pt, value, opcValue.quality().toCommonQuality());
    }

    private void pollAll() {
        if (nodeIds.isEmpty()) return;
        try {
            Map<String, OpcValue> results = client.read(nodeIds);
            results.forEach((nodeId, opcValue) -> {
                PointConfig pt = pointMap.get(nodeId);
                if (pt == null) return;
                Double value = opcValue.toDouble();
                if (value == null) return;
                publish(pt, value, opcValue.quality().toCommonQuality());
            });
        } catch (Exception ex) {
            log.warn("opcua: poll failed: {}", ex.getMessage());
        }
    }

    private void publish(PointConfig pt, double value, String quality) {
        try {
            byte[] data = MAPPER.writeValueAsBytes(
                CommonEvent.now(cfg.connectorId(), pt.localId(), pt.deviceRef(), value, pt.unit(), quality)
            );
            publisher.publish(subject, data);
            log.debug("opcua: published {}={} quality={}", pt.localId(), value, quality);
        } catch (Exception ex) {
            log.warn("opcua: publish failed for {}: {}", pt.localId(), ex.getMessage());
        }
    }
}
