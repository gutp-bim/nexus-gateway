// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import org.eclipse.milo.opcua.sdk.client.OpcUaClient;
import org.eclipse.milo.opcua.sdk.client.api.subscriptions.UaMonitoredItem;
import org.eclipse.milo.opcua.sdk.client.api.subscriptions.UaSubscription;
import org.eclipse.milo.opcua.stack.core.AttributeId;
import org.eclipse.milo.opcua.stack.core.Identifiers;
import org.eclipse.milo.opcua.stack.core.types.builtin.*;
import org.eclipse.milo.opcua.stack.core.types.builtin.unsigned.UInteger;
import org.eclipse.milo.opcua.stack.core.types.enumerated.*;
import org.eclipse.milo.opcua.stack.core.types.structured.*;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.*;
import java.util.concurrent.ExecutionException;
import java.util.function.BiConsumer;

import static org.eclipse.milo.opcua.stack.core.types.builtin.unsigned.Unsigned.uint;
import static org.eclipse.milo.opcua.stack.core.types.builtin.unsigned.Unsigned.ushort;

public class MiloOpcUaClientFacade implements OpcUaClientFacade {

    private static final Logger log = LoggerFactory.getLogger(MiloOpcUaClientFacade.class);

    private final String endpointUrl;
    private OpcUaClient miloClient;

    public MiloOpcUaClientFacade(String endpointUrl) {
        this.endpointUrl = endpointUrl;
    }

    @Override
    public void connect() throws Exception {
        miloClient = OpcUaClient.create(endpointUrl);
        miloClient.connect().get();
        log.info("opcua: connected to {}", endpointUrl);
    }

    @Override
    public void subscribe(List<String> nodeIds, BiConsumer<String, OpcValue> onValue) throws Exception {
        UaSubscription subscription = miloClient.getSubscriptionManager()
            .createSubscription(1000.0) // 1 second publishing interval
            .get();

        List<ReadValueId> readValueIds = nodeIds.stream()
            .map(id -> new ReadValueId(
                NodeId.parse(id),
                AttributeId.Value.uid(),
                null,
                QualifiedName.NULL_VALUE
            ))
            .toList();

        List<MonitoringParameters> params = new ArrayList<>();
        for (int i = 0; i < readValueIds.size(); i++) {
            params.add(new MonitoringParameters(
                uint(i + 1),
                250.0,  // 250ms sampling interval
                null,
                uint(10),
                true
            ));
        }

        List<UaMonitoredItem> items = subscription.createMonitoredItems(
            TimestampsToReturn.Both,
            mapToRequests(readValueIds, params),
            (item, idx) -> item.setValueConsumer((mi, value) -> {
                String nodeId = mi.getReadValueId().getNodeId().toParseableString();
                onValue.accept(nodeId, toOpcValue(value));
            })
        ).get();

        for (UaMonitoredItem item : items) {
            if (item.getStatusCode().isGood()) {
                log.debug("opcua: monitoring {}", item.getReadValueId().getNodeId());
            } else {
                log.warn("opcua: failed to monitor {}: {}", item.getReadValueId().getNodeId(), item.getStatusCode());
            }
        }
    }

    @Override
    public Map<String, OpcValue> read(List<String> nodeIds) throws Exception {
        List<ReadValueId> readValueIds = nodeIds.stream()
            .map(id -> new ReadValueId(
                NodeId.parse(id),
                AttributeId.Value.uid(),
                null,
                QualifiedName.NULL_VALUE
            ))
            .toList();

        DataValue[] values = miloClient.read(0.0, TimestampsToReturn.Source, readValueIds).get().getResults();

        Map<String, OpcValue> result = new LinkedHashMap<>();
        if (values == null) {
            log.warn("opcua: read returned null results array for {} nodes", nodeIds.size());
            return result;
        }
        for (int i = 0; i < Math.min(nodeIds.size(), values.length); i++) {
            result.put(nodeIds.get(i), toOpcValue(values[i]));
        }
        return result;
    }

    @Override
    public Map<String, String> browse(String rootNodeId) throws Exception {
        NodeId root = NodeId.parse(rootNodeId);
        BrowseDescription description = new BrowseDescription(
            root,
            BrowseDirection.Forward,
            Identifiers.HierarchicalReferences,
            true,
            uint(NodeClass.Object.getValue() | NodeClass.Variable.getValue()),
            uint(BrowseResultMask.All.getValue())
        );

        BrowseResult result = miloClient.browse(description).get();
        Map<String, String> nodes = new LinkedHashMap<>();
        if (result.getReferences() != null) {
            for (var ref : result.getReferences()) {
                String nodeId = ref.getNodeId().toParseableString();
                String name = ref.getDisplayName().getText();
                nodes.put(nodeId, name);
            }
        }
        return nodes;
    }

    @Override
    public void writeNode(String nodeId, double value) throws Exception {
        DataValue dv = new DataValue(new Variant(value), null, null);
        WriteValue wv = new WriteValue(NodeId.parse(nodeId), AttributeId.Value.uid(), null, dv);
        StatusCode[] results = miloClient.write(List.of(wv)).get().getResults();
        if (results == null || results.length == 0) {
            throw new Exception("opcua: writeNode returned no results for " + nodeId);
        }
        StatusCode sc = results[0];
        if (sc.isBad()) {
            throw new Exception("opcua: writeNode bad status " + sc + " for " + nodeId);
        }
        log.debug("opcua: writeNode {} = {} status={}", nodeId, value, sc);
    }

    @Override
    public void callMethod(String objectNodeId, String methodNodeId, double value) throws Exception {
        CallMethodRequest req = new CallMethodRequest(
            NodeId.parse(objectNodeId),
            NodeId.parse(methodNodeId),
            new Variant[]{new Variant(value)}
        );
        CallMethodResult[] results = miloClient.call(List.of(req)).get().getResults();
        if (results == null || results.length == 0) {
            throw new Exception("opcua: callMethod returned no results for " + methodNodeId);
        }
        StatusCode sc = results[0].getStatusCode();
        if (sc.isBad()) {
            throw new Exception("opcua: callMethod bad status " + sc + " for " + methodNodeId);
        }
        log.debug("opcua: callMethod {}({}) status={}", methodNodeId, value, sc);
    }

    @Override
    public void close() throws Exception {
        if (miloClient != null) {
            miloClient.disconnect().get();
            log.info("opcua: disconnected from {}", endpointUrl);
        }
    }

    private static OpcValue toOpcValue(DataValue dv) {
        StatusCode sc = dv.getStatusCode();
        if (sc != null && sc.isBad()) return OpcValue.bad();
        Object raw = dv.getValue() != null ? dv.getValue().getValue() : null;
        if (sc != null && sc.isUncertain()) return OpcValue.uncertain(raw);
        return OpcValue.good(raw);
    }

    private static List<MonitoredItemCreateRequest> mapToRequests(
        List<ReadValueId> readValueIds, List<MonitoringParameters> params
    ) {
        List<MonitoredItemCreateRequest> requests = new ArrayList<>();
        for (int i = 0; i < readValueIds.size(); i++) {
            requests.add(new MonitoredItemCreateRequest(
                readValueIds.get(i), MonitoringMode.Reporting, params.get(i)
            ));
        }
        return requests;
    }
}
