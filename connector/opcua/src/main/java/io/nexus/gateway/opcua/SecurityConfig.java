// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package io.nexus.gateway.opcua;

import java.util.Locale;
import java.util.function.Function;

/**
 * Parsed + validated OPC-UA channel-security and user-identity configuration.
 *
 * <p>This value object is <b>pure</b>: {@link #fromEnv(Function)} resolves the enum / identity-mode /
 * combination validity from environment strings only. It performs <b>no</b> network or file I/O — the
 * actual PEM cert/key loading is deferred to {@link MiloOpcUaClientFacade#connect()} so the parsing is
 * unit-testable without a server or on-disk certificates.
 *
 * <p>Environment contract:
 * <ul>
 *   <li>{@code OPCUA_SECURITY_POLICY} — {@code None} (default) | {@code Basic128Rsa15} | {@code Basic256}
 *       | {@code Basic256Sha256}: the channel security policy; the connector selects the server endpoint
 *       whose security policy matches.</li>
 *   <li>{@code OPCUA_SECURITY_MODE} — {@code None} | {@code Sign} | {@code SignAndEncrypt}. Default:
 *       {@code None} when policy is {@code None}, else {@code SignAndEncrypt}.</li>
 *   <li>{@code OPCUA_CLIENT_CERT_FILE} / {@code OPCUA_CLIENT_KEY_FILE} — PEM paths for the client
 *       application instance certificate + its PKCS#8 private key. Used as the app certificate for a
 *       secured channel and (when identity is x509) as the X509 user token.</li>
 *   <li>{@code OPCUA_IDENTITY} — {@code anonymous} (default) | {@code username} | {@code x509}.</li>
 *   <li>{@code OPCUA_USERNAME} / {@code OPCUA_PASSWORD} — username identity credentials.</li>
 * </ul>
 */
public record SecurityConfig(
    Policy policy,
    MessageMode mode,
    IdentityMode identity,
    String username,
    String password,
    String certFile,
    String keyFile
) {
    /** Channel security policy; {@code label} is the exact accepted {@code OPCUA_SECURITY_POLICY} value. */
    public enum Policy {
        NONE("None"),
        BASIC128RSA15("Basic128Rsa15"),
        BASIC256("Basic256"),
        BASIC256SHA256("Basic256Sha256");

        public final String label;
        Policy(String label) { this.label = label; }
    }

    /** Message security mode; {@code label} is the exact accepted {@code OPCUA_SECURITY_MODE} value. */
    public enum MessageMode {
        NONE("None"),
        SIGN("Sign"),
        SIGN_AND_ENCRYPT("SignAndEncrypt");

        public final String label;
        MessageMode(String label) { this.label = label; }
    }

    /** User-identity token type. */
    public enum IdentityMode { ANONYMOUS, USERNAME, X509 }

    /** True when a secured channel is requested (policy other than {@code None}). */
    public boolean isSecured() { return policy != Policy.NONE; }

    /** The default/legacy configuration: {@code None} policy + anonymous identity (today's behaviour). */
    public static SecurityConfig anonymous() {
        return new SecurityConfig(
            Policy.NONE, MessageMode.NONE, IdentityMode.ANONYMOUS, null, null, null, null);
    }

    /**
     * Resolve and validate the security configuration from an environment lookup.
     *
     * @param getenv variable-name → value (blank/absent modelled as {@code null}); pass
     *               {@code System::getenv} in production.
     * @throws IllegalArgumentException with the offending variable named, for any invalid value or combination.
     */
    public static SecurityConfig fromEnv(Function<String, String> getenv) {
        String policyRaw   = val(getenv, "OPCUA_SECURITY_POLICY");
        String modeRaw     = val(getenv, "OPCUA_SECURITY_MODE");
        String identityRaw = val(getenv, "OPCUA_IDENTITY");
        String username    = val(getenv, "OPCUA_USERNAME");
        String password    = val(getenv, "OPCUA_PASSWORD");
        String certFile    = val(getenv, "OPCUA_CLIENT_CERT_FILE");
        String keyFile     = val(getenv, "OPCUA_CLIENT_KEY_FILE");

        Policy policy = (policyRaw == null) ? Policy.NONE : matchPolicy(policyRaw);

        MessageMode mode;
        if (modeRaw == null) {
            mode = (policy == Policy.NONE) ? MessageMode.NONE : MessageMode.SIGN_AND_ENCRYPT;
        } else {
            mode = matchMode(modeRaw);
        }

        // A message security mode (Sign / SignAndEncrypt) requires a real policy.
        if (mode != MessageMode.NONE && policy == Policy.NONE) {
            throw new IllegalArgumentException(
                "OPCUA_SECURITY_MODE: " + mode.label + " requires OPCUA_SECURITY_POLICY != None");
        }

        // Resolve identity mode: explicit OPCUA_IDENTITY wins; otherwise infer from username presence.
        IdentityMode identity;
        if (identityRaw == null) {
            identity = (username != null) ? IdentityMode.USERNAME : IdentityMode.ANONYMOUS;
        } else {
            switch (identityRaw.toLowerCase(Locale.ROOT)) {
                case "anonymous" -> identity = IdentityMode.ANONYMOUS;
                case "username"  -> identity = IdentityMode.USERNAME;
                case "x509"      -> identity = IdentityMode.X509;
                default -> throw new IllegalArgumentException(
                    "OPCUA_IDENTITY: unknown value '" + identityRaw
                        + "' (accepted: anonymous, username, x509)");
            }
        }

        // Username / password must be supplied together (general rule, both inference and explicit paths).
        if (username != null && password == null) {
            throw new IllegalArgumentException(
                "Missing env var: OPCUA_PASSWORD (OPCUA_USERNAME requires OPCUA_PASSWORD)");
        }
        if (password != null && username == null) {
            throw new IllegalArgumentException(
                "Missing env var: OPCUA_USERNAME (OPCUA_PASSWORD requires OPCUA_USERNAME)");
        }

        // Username identity needs the username (password is covered by the pairing rule above).
        if (identity == IdentityMode.USERNAME && username == null) {
            throw new IllegalArgumentException(
                "Missing env var: OPCUA_USERNAME (OPCUA_IDENTITY=username requires OPCUA_USERNAME)");
        }

        // A secured channel needs an application certificate; x509 identity needs a user certificate.
        // Both use the same OPCUA_CLIENT_CERT_FILE / OPCUA_CLIENT_KEY_FILE pair.
        boolean needCert = (policy != Policy.NONE) || (identity == IdentityMode.X509);
        if (needCert) {
            String reason = (policy != Policy.NONE)
                ? "a secured channel needs a client application certificate"
                : "OPCUA_IDENTITY=x509 requires a client certificate";
            if (certFile == null) {
                throw new IllegalArgumentException(
                    "Missing env var: OPCUA_CLIENT_CERT_FILE (" + reason + ")");
            }
            if (keyFile == null) {
                throw new IllegalArgumentException(
                    "Missing env var: OPCUA_CLIENT_KEY_FILE (" + reason + ")");
            }
        }

        return new SecurityConfig(policy, mode, identity, username, password, certFile, keyFile);
    }

    private static Policy matchPolicy(String raw) {
        for (Policy p : Policy.values()) {
            if (p.label.equalsIgnoreCase(raw)) return p;
        }
        throw new IllegalArgumentException(
            "OPCUA_SECURITY_POLICY: unknown value '" + raw
                + "' (accepted: None, Basic128Rsa15, Basic256, Basic256Sha256)");
    }

    private static MessageMode matchMode(String raw) {
        for (MessageMode m : MessageMode.values()) {
            if (m.label.equalsIgnoreCase(raw)) return m;
        }
        throw new IllegalArgumentException(
            "OPCUA_SECURITY_MODE: unknown value '" + raw
                + "' (accepted: None, Sign, SignAndEncrypt)");
    }

    private static String val(Function<String, String> getenv, String key) {
        String v = getenv.apply(key);
        return (v != null && !v.isBlank()) ? v.trim() : null;
    }
}
