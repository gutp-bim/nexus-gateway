// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.BiConsumer;

import static org.junit.jupiter.api.Assertions.*;

class WriteHandlerTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    // ── helpers ───────────────────────────────────────────────────────────────

    static class MockOpcUaClient implements OpcUaClientFacade {
        final List<Object[]> writes = new CopyOnWriteArrayList<>();
        final List<Object[]> methodCalls = new CopyOnWriteArrayList<>();
        Exception writeThrows;
        long writeDelayMs;

        @Override public void connect() {}
        @Override public void close() {}
        @Override public Map<String, OpcValue> read(List<String> nodeIds) { return Map.of(); }
        @Override public void subscribe(List<String> nodeIds, BiConsumer<String, OpcValue> onValue) {}
        @Override public Map<String, String> browse(String rootNodeId) { return Map.of(); }

        @Override
        public void writeNode(String nodeId, double value) throws Exception {
            if (writeDelayMs > 0) Thread.sleep(writeDelayMs);
            if (writeThrows != null) throw writeThrows;
            writes.add(new Object[]{nodeId, value});
        }

        @Override
        public void callMethod(String objectNodeId, String methodNodeId, double value) throws Exception {
            if (writeDelayMs > 0) Thread.sleep(writeDelayMs);
            if (writeThrows != null) throw writeThrows;
            methodCalls.add(new Object[]{objectNodeId, methodNodeId, value});
        }
    }

    static class Replies {
        final List<byte[]> data = new CopyOnWriteArrayList<>();

        WriteHandler.Responder asResponder() {
            return (replyTo, d) -> data.add(d);
        }

        Map<String, Object> last() throws Exception {
            assertFalse(data.isEmpty(), "no reply sent");
            return MAPPER.readValue(data.get(data.size() - 1), new TypeReference<>() {});
        }
    }

    static Config makeConfig(List<PointConfig> points, double writeTimeout) {
        return new Config(
            "opcua-01", "nats://localhost:4222", "opc.tcp://localhost:4840",
            "sim-server", points, 30.0, writeTimeout
        );
    }

    static Config makeConfig(List<PointConfig> points) {
        return makeConfig(points, 10.0);
    }

    static PointConfig writablePoint() {
        return new PointConfig("ns=2;i=1001", "sim-server", "degC", true, null);
    }

    static PointConfig readonlyPoint() {
        return new PointConfig("ns=2;i=1002", "sim-server", "degC", false, null);
    }

    static PointConfig methodPoint() {
        return new PointConfig("ns=2;i=1003", "sim-server", "", true, "ns=2;i=9001");
    }

    static byte[] cmdBytes(String controlId, String localId, double value) throws Exception {
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("control_id", controlId);
        m.put("local_id", localId);
        m.put("device_ref", "sim-server");
        m.put("value", value);
        m.put("priority", 0);
        return MAPPER.writeValueAsBytes(m);
    }

    // ── tests ─────────────────────────────────────────────────────────────────

    @Test
    void writeOkRepliesSuccess() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(writablePoint())), client, replies.asResponder());

        handler.handle(cmdBytes("ctrl-1", "ns=2;i=1001", 21.5), "reply.1");

        Map<String, Object> reply = replies.last();
        assertTrue((Boolean) reply.get("success"));
        assertEquals("ok", reply.get("response"));
        assertEquals(1, client.writes.size());
        assertEquals("ns=2;i=1001", client.writes.get(0)[0]);
        assertEquals(21.5, (Double) client.writes.get(0)[1], 0.001);
    }

    @Test
    void deviceErrorRepliesDeviceError() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        client.writeThrows = new RuntimeException("StatusCode=Bad_NodeIdUnknown");
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(writablePoint())), client, replies.asResponder());

        handler.handle(cmdBytes("ctrl-1", "ns=2;i=1001", 0.0), "reply.1");

        Map<String, Object> reply = replies.last();
        assertFalse((Boolean) reply.get("success"));
        assertTrue(((String) reply.get("response")).startsWith("device_error"));
    }

    @Test
    void timeoutRepliesTimeout() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        client.writeDelayMs = 5_000;
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(
            makeConfig(List.of(writablePoint()), 0.05),
            client, replies.asResponder()
        );

        handler.handle(cmdBytes("ctrl-1", "ns=2;i=1001", 0.0), "reply.1");

        Map<String, Object> reply = replies.last();
        assertFalse((Boolean) reply.get("success"));
        assertEquals("timeout", reply.get("response"));
    }

    @Test
    void unknownLocalIdRepliesNotWritable() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(writablePoint())), client, replies.asResponder());

        handler.handle(cmdBytes("ctrl-1", "ns=2;i=9999", 0.0), "reply.1");

        Map<String, Object> reply = replies.last();
        assertFalse((Boolean) reply.get("success"));
        assertEquals("not_writable", reply.get("response"));
        assertTrue(client.writes.isEmpty());
    }

    @Test
    void readonlyPointRepliesNotWritable() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(readonlyPoint())), client, replies.asResponder());

        handler.handle(cmdBytes("ctrl-1", "ns=2;i=1002", 0.0), "reply.1");

        Map<String, Object> reply = replies.last();
        assertFalse((Boolean) reply.get("success"));
        assertEquals("not_writable", reply.get("response"));
        assertTrue(client.writes.isEmpty());
    }

    @Test
    void duplicateControlIdNoDoubleWrite() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(writablePoint())), client, replies.asResponder());

        handler.handle(cmdBytes("ctrl-dup", "ns=2;i=1001", 5.0), "reply.1");
        handler.handle(cmdBytes("ctrl-dup", "ns=2;i=1001", 5.0), "reply.2");

        assertEquals(1, client.writes.size(), "device must only be written once per control_id");
        assertEquals(2, replies.data.size(), "both messages must receive a reply");
        assertArrayEquals(replies.data.get(0), replies.data.get(1));
    }

    @Test
    void methodPointCallsCallMethod() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(methodPoint())), client, replies.asResponder());

        handler.handle(cmdBytes("ctrl-1", "ns=2;i=1003", 42.0), "reply.1");

        Map<String, Object> reply = replies.last();
        assertTrue((Boolean) reply.get("success"));
        assertTrue(client.writes.isEmpty(), "writeNode must not be called for method points");
        assertEquals(1, client.methodCalls.size());
        assertEquals("ns=2;i=1003", client.methodCalls.get(0)[0]);
        assertEquals("ns=2;i=9001", client.methodCalls.get(0)[1]);
        assertEquals(42.0, (Double) client.methodCalls.get(0)[2], 0.001);
    }

    @Test
    void malformedCommandRepliesBadRequest() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(writablePoint())), client, replies.asResponder());

        handler.handle("not json".getBytes(), "reply.1");

        Map<String, Object> reply = replies.last();
        assertFalse((Boolean) reply.get("success"));
        assertEquals("bad_request", reply.get("response"));
    }

    @Test
    void nullOrBlankReplyToIsNotPublished() throws Exception {
        MockOpcUaClient client = new MockOpcUaClient();
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(writablePoint())), client, replies.asResponder());

        handler.handle(cmdBytes("ctrl-1", "ns=2;i=1001", 1.0), null);

        // no reply should be sent but write still executes
        assertTrue(replies.data.isEmpty());
        assertEquals(1, client.writes.size());
    }

    @Test
    void concurrentDuplicatesDoNotDoubleWrite() throws Exception {
        // Two threads deliver same control_id simultaneously; only one write must occur.
        MockOpcUaClient client = new MockOpcUaClient();
        client.writeDelayMs = 50; // slow enough that both threads arrive before first completes
        Replies replies = new Replies();
        WriteHandler handler = new WriteHandler(makeConfig(List.of(writablePoint()), 5.0), client, replies.asResponder());

        CountDownLatch go = new CountDownLatch(1);
        Runnable task = () -> {
            try {
                go.await();
                handler.handle(cmdBytes("ctrl-concurrent", "ns=2;i=1001", 7.0), "reply.x");
            } catch (Exception e) {
                throw new RuntimeException(e);
            }
        };

        ExecutorService exec = Executors.newFixedThreadPool(2);
        Future<?> f1 = exec.submit(task);
        Future<?> f2 = exec.submit(task);
        go.countDown();
        f1.get(3, TimeUnit.SECONDS);
        f2.get(3, TimeUnit.SECONDS);
        exec.shutdown();

        assertTrue(client.writes.size() <= 1, "concurrent duplicates must not double-write");
    }
}
