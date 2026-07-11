// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.util.List;
import java.util.Map;

public record Config(
    String connectorId,
    String natsUrl,
    String opcuaEndpoint,
    String deviceRef,
    List<PointConfig> points,
    double pollInterval,
    double writeTimeout,
    SecurityConfig security
) {
    public Config {
        if (pollInterval <= 0)  throw new IllegalArgumentException("pollInterval must be positive, got " + pollInterval);
        if (writeTimeout <= 0)  throw new IllegalArgumentException("writeTimeout must be positive, got " + writeTimeout);
        if (security == null)   security = SecurityConfig.anonymous();
    }

    /**
     * Backward-compatible constructor defaulting to the legacy {@code None} policy + anonymous identity
     * (today's behaviour). Used by existing tests that predate the security config (#32).
     */
    public Config(
        String connectorId,
        String natsUrl,
        String opcuaEndpoint,
        String deviceRef,
        List<PointConfig> points,
        double pollInterval,
        double writeTimeout
    ) {
        this(connectorId, natsUrl, opcuaEndpoint, deviceRef, points, pollInterval, writeTimeout,
            SecurityConfig.anonymous());
    }

    private static final ObjectMapper MAPPER = new ObjectMapper();

    public static Config fromEnv() throws Exception {
        String connectorId = required("CONNECTOR_ID");
        String natsUrl = env("NATS_URL", "nats://localhost:4222");
        String endpoint = required("OPCUA_ENDPOINT");
        String deviceRef = env("OPCUA_DEVICE_REF", "opcua-server");
        double pollInterval = Double.parseDouble(env("OPCUA_POLL_INTERVAL", "60"));
        if (pollInterval <= 0) {
            throw new IllegalArgumentException("OPCUA_POLL_INTERVAL must be positive");
        }
        double writeTimeout = Double.parseDouble(env("OPCUA_WRITE_TIMEOUT", "10"));
        if (writeTimeout <= 0) {
            throw new IllegalArgumentException("OPCUA_WRITE_TIMEOUT must be positive");
        }

        String pointsJson = env("OPCUA_POINTS", "[]");
        List<Map<String, Object>> rawPoints = MAPPER.readValue(
            pointsJson, new TypeReference<>() {}
        );
        List<PointConfig> points = rawPoints.stream()
            .map(p -> {
                String localId = (String) p.get("local_id");
                if (localId == null || localId.isBlank()) {
                    throw new IllegalArgumentException("point entry missing required 'local_id': " + p);
                }
                return new PointConfig(
                    localId,
                    (String) p.getOrDefault("device_ref", deviceRef),
                    (String) p.getOrDefault("unit", ""),
                    Boolean.TRUE.equals(p.get("writable")),
                    (String) p.get("method_node_id")
                );
            })
            .toList();

        SecurityConfig security = SecurityConfig.fromEnv(System::getenv);

        return new Config(connectorId, natsUrl, endpoint, deviceRef, points, pollInterval, writeTimeout, security);
    }

    private static String required(String key) {
        String v = System.getenv(key);
        if (v == null || v.isBlank()) throw new IllegalArgumentException("Missing env var: " + key);
        return v;
    }

    private static String env(String key, String def) {
        String v = System.getenv(key);
        return (v != null && !v.isBlank()) ? v : def;
    }
}
