// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

public record OpcValue(Object rawValue, OpcQuality quality) {

    public static OpcValue good(Object value) {
        return new OpcValue(value, OpcQuality.GOOD);
    }

    public static OpcValue uncertain(Object value) {
        return new OpcValue(value, OpcQuality.UNCERTAIN);
    }

    public static OpcValue bad() {
        return new OpcValue(null, OpcQuality.BAD);
    }

    /** Convert rawValue to double. Boolean maps to 1.0/0.0. Returns null on failure. */
    public Double toDouble() {
        if (rawValue == null) return null;
        if (rawValue instanceof Boolean b) return b ? 1.0 : 0.0;
        if (rawValue instanceof Number n) return n.doubleValue();
        try {
            return Double.parseDouble(rawValue.toString());
        } catch (NumberFormatException e) {
            return null;
        }
    }

    /** Return a JSON telemetry scalar while preserving native boolean/string types. */
    public Object toTelemetryScalar() {
        if (rawValue == null) return null;
        if (rawValue instanceof Boolean || rawValue instanceof String) return rawValue;
        if (rawValue instanceof Number n) {
            double value = n.doubleValue();
            return Double.isFinite(value) ? value : null;
        }
        return null;
    }
}
