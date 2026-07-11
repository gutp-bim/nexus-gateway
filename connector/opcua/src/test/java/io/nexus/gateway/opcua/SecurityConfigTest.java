// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;

import java.util.HashMap;
import java.util.Map;
import java.util.function.Function;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.*;

/** Table-driven validation matrix for {@link SecurityConfig#fromEnv} (#32). Pure — no server, no files. */
class SecurityConfigTest {

    private static Function<String, String> env(Map<String, String> m) {
        return m::get;
    }

    private static Map<String, String> map(String... kv) {
        Map<String, String> m = new HashMap<>();
        for (int i = 0; i < kv.length; i += 2) m.put(kv[i], kv[i + 1]);
        return m;
    }

    // ---- default (no vars) -------------------------------------------------

    @Test
    void defaultIsNonePolicyAnonymous() {
        SecurityConfig c = SecurityConfig.fromEnv(env(Map.of()));
        assertEquals(SecurityConfig.Policy.NONE, c.policy());
        assertEquals(SecurityConfig.MessageMode.NONE, c.mode());
        assertEquals(SecurityConfig.IdentityMode.ANONYMOUS, c.identity());
        assertFalse(c.isSecured());
        assertNull(c.username());
        assertNull(c.certFile());
    }

    // ---- valid combinations ------------------------------------------------

    @Test
    void securedPolicyDefaultsModeToSignAndEncrypt() {
        SecurityConfig c = SecurityConfig.fromEnv(env(map(
            "OPCUA_SECURITY_POLICY", "Basic256Sha256",
            "OPCUA_CLIENT_CERT_FILE", "/certs/client.pem",
            "OPCUA_CLIENT_KEY_FILE", "/certs/client.key")));
        assertEquals(SecurityConfig.Policy.BASIC256SHA256, c.policy());
        assertEquals(SecurityConfig.MessageMode.SIGN_AND_ENCRYPT, c.mode());
        assertEquals(SecurityConfig.IdentityMode.ANONYMOUS, c.identity());
        assertTrue(c.isSecured());
    }

    @Test
    void usernameInferredFromCredentials() {
        SecurityConfig c = SecurityConfig.fromEnv(env(map(
            "OPCUA_USERNAME", "alice",
            "OPCUA_PASSWORD", "secret")));
        assertEquals(SecurityConfig.IdentityMode.USERNAME, c.identity());
        assertEquals(SecurityConfig.Policy.NONE, c.policy());
        assertEquals("alice", c.username());
    }

    @Test
    void explicitSignModeWithPolicyParses() {
        SecurityConfig c = SecurityConfig.fromEnv(env(map(
            "OPCUA_SECURITY_POLICY", "Basic256",
            "OPCUA_SECURITY_MODE", "Sign",
            "OPCUA_CLIENT_CERT_FILE", "/certs/client.pem",
            "OPCUA_CLIENT_KEY_FILE", "/certs/client.key")));
        assertEquals(SecurityConfig.MessageMode.SIGN, c.mode());
    }

    @Test
    void x509IdentityWithNonePolicyRequiresCertOnly() {
        SecurityConfig c = SecurityConfig.fromEnv(env(map(
            "OPCUA_IDENTITY", "x509",
            "OPCUA_CLIENT_CERT_FILE", "/certs/user.pem",
            "OPCUA_CLIENT_KEY_FILE", "/certs/user.key")));
        assertEquals(SecurityConfig.IdentityMode.X509, c.identity());
        assertEquals(SecurityConfig.Policy.NONE, c.policy());
    }

    // ---- invalid combinations: each must name the offending variable -------

    static Stream<Arguments> invalidCases() {
        return Stream.of(
            Arguments.of("unknown policy",
                map("OPCUA_SECURITY_POLICY", "Bogus"), "OPCUA_SECURITY_POLICY"),
            Arguments.of("unknown mode",
                map("OPCUA_SECURITY_POLICY", "Basic256", "OPCUA_SECURITY_MODE", "Bogus",
                    "OPCUA_CLIENT_CERT_FILE", "/c.pem", "OPCUA_CLIENT_KEY_FILE", "/c.key"),
                "OPCUA_SECURITY_MODE"),
            Arguments.of("sign mode without a real policy",
                map("OPCUA_SECURITY_MODE", "SignAndEncrypt"), "OPCUA_SECURITY_MODE"),
            Arguments.of("secured policy without cert",
                map("OPCUA_SECURITY_POLICY", "Basic256"), "OPCUA_CLIENT_CERT_FILE"),
            Arguments.of("secured policy without key",
                map("OPCUA_SECURITY_POLICY", "Basic256", "OPCUA_CLIENT_CERT_FILE", "/c.pem"),
                "OPCUA_CLIENT_KEY_FILE"),
            Arguments.of("username without password",
                map("OPCUA_USERNAME", "alice"), "OPCUA_PASSWORD"),
            Arguments.of("password without username",
                map("OPCUA_PASSWORD", "secret"), "OPCUA_USERNAME"),
            Arguments.of("identity=username without username",
                map("OPCUA_IDENTITY", "username"), "OPCUA_USERNAME"),
            Arguments.of("identity=x509 without cert",
                map("OPCUA_IDENTITY", "x509"), "OPCUA_CLIENT_CERT_FILE"),
            Arguments.of("identity=x509 without key",
                map("OPCUA_IDENTITY", "x509", "OPCUA_CLIENT_CERT_FILE", "/u.pem"),
                "OPCUA_CLIENT_KEY_FILE"),
            Arguments.of("unknown identity",
                map("OPCUA_IDENTITY", "kerberos"), "OPCUA_IDENTITY")
        );
    }

    @ParameterizedTest(name = "{0}")
    @MethodSource("invalidCases")
    void invalidCombinationsThrowNamingVariable(String desc, Map<String, String> vars, String expectedVar) {
        IllegalArgumentException ex = assertThrows(IllegalArgumentException.class,
            () -> SecurityConfig.fromEnv(env(vars)), desc);
        assertTrue(ex.getMessage().contains(expectedVar),
            "message for [" + desc + "] must name " + expectedVar + " but was: " + ex.getMessage());
    }
}
