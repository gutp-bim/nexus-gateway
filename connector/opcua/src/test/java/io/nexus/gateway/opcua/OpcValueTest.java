// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class OpcValueTest {

    @Test void goodDouble()   { assertEquals(21.5, OpcValue.good(21.5).toDouble()); }
    @Test void goodFloat()    { assertEquals(21.5, OpcValue.good(21.5f).toDouble(), 0.001); }
    @Test void goodInt()      { assertEquals(42.0, OpcValue.good(42).toDouble()); }
    @Test void goodLong()     { assertEquals(1000.0, OpcValue.good(1000L).toDouble()); }
    @Test void goodBoolTrue() { assertEquals(true, OpcValue.good(true).toTelemetryScalar()); }
    @Test void goodBoolFalse(){ assertEquals(false, OpcValue.good(false).toTelemetryScalar()); }
    @Test void goodString()   { assertEquals("running", OpcValue.good("running").toTelemetryScalar()); }
    @Test void badNullValue() { assertNull(OpcValue.bad().toDouble()); }
    @Test void goodNonNumericString() { assertEquals("not-a-number", OpcValue.good("not-a-number").toTelemetryScalar()); }

    @Test void qualityMapping() {
        assertEquals("Good",      OpcQuality.GOOD.toCommonQuality());
        assertEquals("Uncertain", OpcQuality.UNCERTAIN.toCommonQuality());
        assertEquals("Bad",       OpcQuality.BAD.toCommonQuality());
    }
}
