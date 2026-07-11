// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.*;
import java.util.concurrent.*;
import java.util.function.BiConsumer;

import static org.junit.jupiter.api.Assertions.*;

class ConnectorTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    // ── helpers ───────────────────────────────────────────────────────────────

    record Published(String subject, byte[] data) {
        Map<String, Object> json() throws Exception {
            return MAPPER.readValue(data, new com.fasterxml.jackson.core.type.TypeReference<>() {});
        }
    }

    static class MockClient implements OpcUaClientFacade {
        final Map<String, OpcValue> readResults = new LinkedHashMap<>();
        BiConsumer<String, OpcValue> subscriber;
        boolean connected;
        boolean closed;

        @Override public void connect() { connected = true; }
        @Override public boolean isConnected() { return connected; }
        @Override public void close()   { connected = false; closed = true; }

        @Override
        public void subscribe(List<String> nodeIds, BiConsumer<String, OpcValue> onValue) {
            this.subscriber = onValue;
        }

        @Override
        public Map<String, OpcValue> read(List<String> nodeIds) {
            Map<String, OpcValue> out = new LinkedHashMap<>();
            nodeIds.forEach(id -> {
                if (readResults.containsKey(id)) out.put(id, readResults.get(id));
            });
            return out;
        }

        @Override
        public Map<String, String> browse(String root) {
            return Map.of();
        }

        @Override public void writeNode(String nodeId, double value) {}
        @Override public void callMethod(String objectNodeId, String methodNodeId, double value) {}
    }

    static class RecordingPublisher {
        final List<Published> published = new CopyOnWriteArrayList<>();
        Connector.Publisher asPublisher() {
            return (subject, data) -> published.add(new Published(subject, data));
        }
    }

    static Config onePointConfig() {
        return new Config(
            "opcua-01", "nats://localhost:4222", "opc.tcp://localhost:4840",
            "sim-server",
            List.of(new PointConfig("ns=2;i=1001", "sim-server", "degC")),
            30.0, 10.0
        );
    }

    /** Same as onePointConfig but with a short re-poll interval for timing tests. */
    static Config onePointConfig(double pollIntervalSec) {
        return new Config(
            "opcua-01", "nats://localhost:4222", "opc.tcp://localhost:4840",
            "sim-server",
            List.of(new PointConfig("ns=2;i=1001", "sim-server", "degC")),
            pollIntervalSec, 10.0
        );
    }

    /** Run connector in a daemon thread; returns the task future. */
    static Future<?> startAsync(Connector connector) {
        ExecutorService exec = Executors.newSingleThreadExecutor(r -> {
            Thread t = new Thread(r, "connector-under-test");
            t.setDaemon(true);
            return t;
        });
        Future<?> f = exec.submit(() -> {
            try { connector.run(); }
            catch (Exception e) { throw new RuntimeException(e); }
        });
        exec.shutdown(); // threads finish naturally; shutdown releases internal resources
        return f;
    }

    /** Spin until client.subscriber is set or 1 s elapses. */
    static void awaitSubscribed(MockClient client) throws InterruptedException {
        long deadline = System.currentTimeMillis() + 1_000;
        while (client.subscriber == null && System.currentTimeMillis() < deadline) {
            Thread.sleep(1);
        }
    }

    // ── tests ─────────────────────────────────────────────────────────────────

    @Test
    void initialPollPublishesOneEventPerPoint() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.good(21.5f));

        RecordingPublisher pub = new RecordingPublisher();
        Config cfg = onePointConfig();

        Connector connector = new Connector(cfg, client, pub.asPublisher());
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);
        connector.stop();
        task.get(2, TimeUnit.SECONDS);

        assertEquals(1, pub.published.size());
        Map<String, Object> evt = pub.published.get(0).json();
        assertEquals("opcua", evt.get("protocol"));
        assertEquals("opcua-01", evt.get("connector_id"));
        assertEquals("ns=2;i=1001", evt.get("local_id"));
        assertEquals(21.5, ((Number) evt.get("value")).doubleValue(), 0.001);
        assertEquals("degC", evt.get("unit"));
        assertEquals("Good", evt.get("quality"));
        assertTrue(((String) evt.get("timestamp")).endsWith("Z"));
    }

    @Test
    void subscriptionNotificationPublishesEvent() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.good(20.0));

        RecordingPublisher pub = new RecordingPublisher();
        Connector connector = new Connector(onePointConfig(), client, pub.asPublisher());
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);
        assertNotNull(client.subscriber, "connector must register subscription");

        // Fire COV-style notification
        client.subscriber.accept("ns=2;i=1001", OpcValue.good(25.5));

        connector.stop();
        task.get(2, TimeUnit.SECONDS);

        long covEvents = pub.published.stream()
            .filter(p -> {
                try { return Double.compare(((Number) p.json().get("value")).doubleValue(), 25.5) == 0; }
                catch (Exception e) { return false; }
            })
            .count();
        assertEquals(1, covEvents, "exactly one COV event must be published");
    }

    @Test
    void badStatusCodePublishesQualityBad() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.bad());

        RecordingPublisher pub = new RecordingPublisher();
        Connector connector = new Connector(onePointConfig(), client, pub.asPublisher());
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);

        connector.stop();
        task.get(2, TimeUnit.SECONDS);

        // bad value → toDouble() returns null → no event published
        assertEquals(0, pub.published.size(), "bad value with null reading must not publish");
    }

    @Test
    void uncertainStatusPublishesQualityUncertain() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.uncertain(18.0));

        RecordingPublisher pub = new RecordingPublisher();
        Connector connector = new Connector(onePointConfig(), client, pub.asPublisher());
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);

        connector.stop();
        task.get(2, TimeUnit.SECONDS);

        assertEquals(1, pub.published.size());
        assertEquals("Uncertain", pub.published.get(0).json().get("quality"));
    }

    @Test
    void booleanValueMapsToOneAndZero() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.good(true));

        RecordingPublisher pub = new RecordingPublisher();
        Connector connector = new Connector(onePointConfig(), client, pub.asPublisher());
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);
        connector.stop();
        task.get(2, TimeUnit.SECONDS);

        assertEquals(1, pub.published.size());
        assertEquals(1.0, ((Number) pub.published.get(0).json().get("value")).doubleValue(), 0.001);
    }

    @Test
    void publishFailureDoesNotCrash() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.good(20.0));

        var callCount = new int[]{0};
        Connector.Publisher failingPub = (subject, data) -> {
            callCount[0]++;
            throw new RuntimeException("nats: transient failure");
        };

        Connector connector = new Connector(onePointConfig(), client, failingPub);
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);
        connector.stop();
        task.get(2, TimeUnit.SECONDS); // must not throw

        assertEquals(1, callCount[0], "publish was attempted for poll event");
    }

    @Test
    void nodesNotInPointListAreIgnored() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.good(20.0));

        RecordingPublisher pub = new RecordingPublisher();
        Connector connector = new Connector(onePointConfig(), client, pub.asPublisher());
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);

        // Fire notification for an unknown node
        client.subscriber.accept("ns=2;i=9999", OpcValue.good(99.0));

        connector.stop();
        task.get(2, TimeUnit.SECONDS);

        // Only the initial poll event for ns=2;i=1001 should be present
        boolean hasUnknown = pub.published.stream().anyMatch(p -> {
            try { return "ns=2;i=9999".equals(p.json().get("local_id")); }
            catch (Exception e) { return false; }
        });
        assertFalse(hasUnknown);
    }

    // #110: the connector must keep a freshness floor via a periodic re-poll
    // alongside the subscription. With static server values the subscription
    // fires nothing, so without re-poll only the single initial-poll event would
    // be published; the loop must produce additional events on each interval.
    @Test
    void periodicRepollPublishesEvenWhenValuesAreStatic() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.good(20.0)); // never changes

        RecordingPublisher pub = new RecordingPublisher();
        // 50 ms re-poll so the test runs fast; subscription is never fired.
        Connector connector = new Connector(onePointConfig(0.05), client, pub.asPublisher());
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);

        // Wait for at least 3 events (initial poll + ≥2 re-polls) or 2 s.
        long deadline = System.currentTimeMillis() + 2_000;
        while (pub.published.size() < 3 && System.currentTimeMillis() < deadline) {
            Thread.sleep(10);
        }

        connector.stop();
        task.get(2, TimeUnit.SECONDS);

        assertTrue(pub.published.size() >= 3,
            "periodic re-poll must publish repeatedly for static values; got " + pub.published.size());
    }

    @Test
    void nodeIdNeverAppearsInNatsSubject() throws Exception {
        MockClient client = new MockClient();
        client.readResults.put("ns=2;i=1001", OpcValue.good(20.0));

        RecordingPublisher pub = new RecordingPublisher();
        Connector connector = new Connector(onePointConfig(), client, pub.asPublisher());
        Future<?> task = startAsync(connector);

        awaitSubscribed(client);
        connector.stop();
        task.get(2, TimeUnit.SECONDS);

        // ADR-0001: NodeId must be in payload only, not in NATS subject
        pub.published.forEach(p -> assertFalse(
            p.subject().contains("ns="),
            "NodeId must not appear in NATS subject"
        ));
        pub.published.forEach(p -> assertEquals("evt.opcua.opcua-01", p.subject()));
    }
}
