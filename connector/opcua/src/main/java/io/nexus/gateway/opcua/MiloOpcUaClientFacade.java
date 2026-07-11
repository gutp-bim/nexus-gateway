// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import org.eclipse.milo.opcua.sdk.client.OpcUaClient;
import org.eclipse.milo.opcua.sdk.client.SessionActivityListener;
import org.eclipse.milo.opcua.sdk.client.api.UaSession;
import org.eclipse.milo.opcua.sdk.client.api.config.OpcUaClientConfigBuilder;
import org.eclipse.milo.opcua.sdk.client.api.identity.AnonymousProvider;
import org.eclipse.milo.opcua.sdk.client.api.identity.IdentityProvider;
import org.eclipse.milo.opcua.sdk.client.api.identity.UsernameProvider;
import org.eclipse.milo.opcua.sdk.client.api.identity.X509IdentityProvider;
import org.eclipse.milo.opcua.sdk.client.api.subscriptions.UaMonitoredItem;
import org.eclipse.milo.opcua.sdk.client.api.subscriptions.UaSubscription;
import org.eclipse.milo.opcua.stack.core.AttributeId;
import org.eclipse.milo.opcua.stack.core.Identifiers;
import org.eclipse.milo.opcua.stack.core.security.SecurityPolicy;
import org.eclipse.milo.opcua.stack.core.types.builtin.*;
import org.eclipse.milo.opcua.stack.core.types.builtin.unsigned.UInteger;
import org.eclipse.milo.opcua.stack.core.types.enumerated.*;
import org.eclipse.milo.opcua.stack.core.types.structured.*;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.PrivateKey;
import java.security.PublicKey;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;
import java.security.interfaces.RSAPrivateCrtKey;
import java.security.spec.PKCS8EncodedKeySpec;
import java.security.spec.RSAPublicKeySpec;
import java.util.*;
import java.util.concurrent.ExecutionException;
import java.util.function.BiConsumer;
import java.util.function.Function;

import static org.eclipse.milo.opcua.stack.core.types.builtin.unsigned.Unsigned.uint;
import static org.eclipse.milo.opcua.stack.core.types.builtin.unsigned.Unsigned.ushort;

public class MiloOpcUaClientFacade implements OpcUaClientFacade {

    private static final Logger log = LoggerFactory.getLogger(MiloOpcUaClientFacade.class);

    private final String endpointUrl;
    private final SecurityConfig security;
    private OpcUaClient miloClient;
    private volatile boolean connected;

    /** Legacy constructor: no security → {@code None} policy + anonymous identity (unchanged behaviour). */
    public MiloOpcUaClientFacade(String endpointUrl) {
        this(endpointUrl, SecurityConfig.anonymous());
    }

    public MiloOpcUaClientFacade(String endpointUrl, SecurityConfig security) {
        this.endpointUrl = endpointUrl;
        this.security = (security != null) ? security : SecurityConfig.anonymous();
    }

    @Override
    public void connect() throws Exception {
        connected = false;
        try {
            miloClient = buildClient();
            // Track live session state so /health flips to degraded when the session
            // drops after startup (server down / partition), not just on close (#35).
            // Registered before connect() so no early transition is missed.
            miloClient.addSessionActivityListener(new SessionActivityListener() {
                @Override
                public void onSessionActive(UaSession session) {
                    connected = true;
                }

                @Override
                public void onSessionInactive(UaSession session) {
                    connected = false;
                    log.warn("opcua: session inactive — /health now degraded until reconnect");
                }
            });
            miloClient.connect().get();
            connected = true;
            log.info("opcua: connected to {}", endpointUrl);
        } catch (Exception e) {
            connected = false;
            throw e;
        }
    }

    @Override
    public boolean isConnected() {
        return connected;
    }

    /**
     * Build the {@link OpcUaClient}. The default (None policy + anonymous identity) path is byte-for-byte
     * today's {@code OpcUaClient.create(endpointUrl)} so the simulator / integration path is unchanged.
     * Otherwise the 3-arg factory selects the endpoint matching the configured policy + mode, sets the
     * identity provider, and (for a secured channel) installs the application certificate + key pair.
     */
    private OpcUaClient buildClient() throws Exception {
        if (security.policy() == SecurityConfig.Policy.NONE
            && security.identity() == SecurityConfig.IdentityMode.ANONYMOUS) {
            return OpcUaClient.create(endpointUrl);
        }

        SecurityPolicy wantPolicy = toMiloPolicy(security.policy());
        MessageSecurityMode wantMode = toMiloMode(security.mode());

        // Load PEM material once if either a secured channel or an x509 user token needs it.
        X509Certificate cert = null;
        KeyPair keyPair = null;
        if (security.isSecured() || security.identity() == SecurityConfig.IdentityMode.X509) {
            cert = loadCertificate(security.certFile());
            PrivateKey privateKey = loadPrivateKey(security.keyFile());
            keyPair = new KeyPair(publicKeyOf(cert, privateKey), privateKey);
        }
        final X509Certificate appCert = cert;
        final KeyPair appKeyPair = keyPair;

        Function<List<EndpointDescription>, Optional<EndpointDescription>> selector = endpoints -> {
            log.info("opcua: {} endpoints offered; selecting policy={} mode={}",
                endpoints.size(), security.policy().label, security.mode().label);
            return endpoints.stream()
                .filter(e -> wantPolicy.getUri().equals(e.getSecurityPolicyUri()))
                .filter(e -> wantMode.equals(e.getSecurityMode()))
                .findFirst();
        };

        IdentityProvider identityProvider = switch (security.identity()) {
            case ANONYMOUS -> new AnonymousProvider();
            case USERNAME  -> new UsernameProvider(security.username(), security.password());
            case X509      -> new X509IdentityProvider(appCert, appKeyPair.getPrivate());
        };

        Function<OpcUaClientConfigBuilder, org.eclipse.milo.opcua.sdk.client.api.config.OpcUaClientConfig>
            configure = builder -> {
                builder.setIdentityProvider(identityProvider);
                if (appCert != null) {
                    builder.setCertificate(appCert);
                    builder.setKeyPair(appKeyPair);
                }
                return builder.build();
            };

        try {
            return OpcUaClient.create(endpointUrl, selector, configure);
        } catch (org.eclipse.milo.opcua.stack.core.UaException e) {
            throw new IllegalStateException(
                "opcua: no server endpoint matches OPCUA_SECURITY_POLICY=" + security.policy().label
                    + " / OPCUA_SECURITY_MODE=" + security.mode().label + " at " + endpointUrl, e);
        }
    }

    private static SecurityPolicy toMiloPolicy(SecurityConfig.Policy p) {
        return switch (p) {
            case NONE           -> SecurityPolicy.None;
            case BASIC128RSA15  -> SecurityPolicy.Basic128Rsa15;
            case BASIC256       -> SecurityPolicy.Basic256;
            case BASIC256SHA256 -> SecurityPolicy.Basic256Sha256;
        };
    }

    private static MessageSecurityMode toMiloMode(SecurityConfig.MessageMode m) {
        return switch (m) {
            case NONE            -> MessageSecurityMode.None;
            case SIGN            -> MessageSecurityMode.Sign;
            case SIGN_AND_ENCRYPT -> MessageSecurityMode.SignAndEncrypt;
        };
    }

    /** Load an X.509 certificate from a PEM (or DER) file; names {@code OPCUA_CLIENT_CERT_FILE} on failure. */
    private static X509Certificate loadCertificate(String certFile) {
        try {
            byte[] bytes = Files.readAllBytes(Path.of(certFile));
            CertificateFactory cf = CertificateFactory.getInstance("X.509");
            return (X509Certificate) cf.generateCertificate(new java.io.ByteArrayInputStream(bytes));
        } catch (Exception e) {
            throw new IllegalArgumentException(
                "OPCUA_CLIENT_CERT_FILE: cannot load client certificate from '" + certFile
                    + "': " + e.getMessage(), e);
        }
    }

    /** Load a PKCS#8 RSA private key from a PEM file; names {@code OPCUA_CLIENT_KEY_FILE} on failure. */
    private static PrivateKey loadPrivateKey(String keyFile) {
        try {
            String pem = Files.readString(Path.of(keyFile));
            String base64 = pem
                .replaceAll("-----BEGIN (RSA )?PRIVATE KEY-----", "")
                .replaceAll("-----END (RSA )?PRIVATE KEY-----", "")
                .replaceAll("\\s", "");
            byte[] der = Base64.getDecoder().decode(base64);
            PKCS8EncodedKeySpec spec = new PKCS8EncodedKeySpec(der);
            return KeyFactory.getInstance("RSA").generatePrivate(spec);
        } catch (Exception e) {
            throw new IllegalArgumentException(
                "OPCUA_CLIENT_KEY_FILE: cannot load PKCS#8 private key from '" + keyFile
                    + "': " + e.getMessage(), e);
        }
    }

    /**
     * Public key for the app key pair. Prefer the certificate's public key; if it is unavailable, derive
     * it from the RSA private key's modulus/exponent so the {@link KeyPair} is still well-formed.
     */
    private static PublicKey publicKeyOf(X509Certificate cert, PrivateKey privateKey) throws Exception {
        if (cert != null && cert.getPublicKey() != null) {
            return cert.getPublicKey();
        }
        if (privateKey instanceof RSAPrivateCrtKey crt) {
            RSAPublicKeySpec spec = new RSAPublicKeySpec(crt.getModulus(), crt.getPublicExponent());
            return KeyFactory.getInstance("RSA").generatePublic(spec);
        }
        throw new IllegalArgumentException(
            "OPCUA_CLIENT_KEY_FILE: cannot derive a public key for the client key pair");
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
        connected = false;
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
