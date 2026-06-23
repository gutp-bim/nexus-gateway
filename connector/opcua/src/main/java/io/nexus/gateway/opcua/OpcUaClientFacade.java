// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import java.util.List;
import java.util.Map;
import java.util.function.BiConsumer;

/** Minimal OPC-UA operations needed by Connector. Isolates Milo from test code. */
public interface OpcUaClientFacade {

    void connect() throws Exception;

    /** Subscribe to monitored items; callback fires on each value change. */
    void subscribe(List<String> nodeIds, BiConsumer<String, OpcValue> onValue) throws Exception;

    /** One-shot read of the given node IDs. */
    Map<String, OpcValue> read(List<String> nodeIds) throws Exception;

    /** Browse children of rootNodeId; returns map of nodeId → display name. */
    Map<String, String> browse(String rootNodeId) throws Exception;

    /** Write a scalar double to a node's Value attribute. Throws on bad StatusCode or transport error. */
    void writeNode(String nodeId, double value) throws Exception;

    /** Call a method node with a single scalar double argument. Throws on bad StatusCode or transport error. */
    void callMethod(String objectNodeId, String methodNodeId, double value) throws Exception;

    void close() throws Exception;
}
