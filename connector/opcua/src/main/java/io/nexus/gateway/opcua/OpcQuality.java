// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

public enum OpcQuality {
    GOOD, UNCERTAIN, BAD;

    public String toCommonQuality() {
        return switch (this) {
            case GOOD -> "Good";
            case UNCERTAIN -> "Uncertain";
            case BAD -> "Bad";
        };
    }
}
