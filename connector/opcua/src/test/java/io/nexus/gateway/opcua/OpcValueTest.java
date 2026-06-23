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
    @Test void goodBoolTrue() { assertEquals(1.0, OpcValue.good(true).toDouble()); }
    @Test void goodBoolFalse(){ assertEquals(0.0, OpcValue.good(false).toDouble()); }
    @Test void goodString()   { assertEquals(3.14, OpcValue.good("3.14").toDouble(), 0.001); }
    @Test void badNullValue() { assertNull(OpcValue.bad().toDouble()); }
    @Test void goodNonNumericString() { assertNull(OpcValue.good("not-a-number").toDouble()); }

    @Test void qualityMapping() {
        assertEquals("Good",      OpcQuality.GOOD.toCommonQuality());
        assertEquals("Uncertain", OpcQuality.UNCERTAIN.toCommonQuality());
        assertEquals("Bad",       OpcQuality.BAD.toCommonQuality());
    }
}
