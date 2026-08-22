# EP-006: Point List Sync & Shared Resolver

**Status:** Prod — backlog audited & closed 2026-07-12 (residuals/deferred items marked inline)
**Priority:** P0

## Goal

The Point List's single source of truth is the Building OS twin (OxiGraph `sbco:PointExt`); the gateway holds a synced copy and converges by diffing against the authoritative snapshot (ADR-0003). This epic delivers that sync loop plus the **shared resolver** consumed by both directions: the Normalizer (`local_id` → `point_id`, EP-003) and the control dispatcher (`point_id` → native, EP-005). The gateway never authors the Point List.

## Acceptance Criteria

- [x] Gateway polls a cheap version token and fetches the authoritative Point List snapshot only when it changes (gateway-scoped, versioned provisioning API — `gutp-building-os-oss` #224).
- [x] On snapshot change, the gateway **diffs** against its local copy and reconverges: Normalizer mapping reload + Connector poll/subscribe list reload, without restarting components. — *Normalizer mapping reload has landed since this epic closed. Connector poll/subscribe list reload landed for the MQTT connector under #119 (docs/adr/0008): `internal/mqttsync` derives the desired subscription set from this same resolver and pushes it to a running MQTT connector over `cfg.mqtt.<id>.apply`, which converges live (Subscribe/Unsubscribe) without a restart, atomically retaining the previous subscriptions on failure. BACnet/OPC-UA/WebSocket/ZeroMQ connectors still pick up Point List changes on restart only — deferred, acceptable at MVP scale.*
- [x] One shared resolver serves both lookups: `local_id`→`point_id` (Normalizer) and `point_id`→native `(protocol, device/object/instance)` + writeability/control schema (control dispatch).
- [x] The synced copy survives gateway restart (local persistence) and reconverges on next poll.
- [x] Point List drift vs Building OS surfaces operationally as the per-`point_id` drift counter on the Ingress uplink (`accepted < sent`, EP-003) — no separate reconciliation protocol.
- [x] Per `smartbuilding_datamodel_builder`: 1 row = 1 Point, mapping `point_id` ⇄ `local_id`, unit, writeability, control schema, device grouping, spatial context.

## Child Features

- [x] FEAT-025: Point List snapshot client (version-token poll, snapshot fetch, local persistence)
  <!-- Known issue (#144, fixed): TestHTTPClient_ZeroOptionsKeepsSystemRootVerification
       pinned the TLS rejection reason to errors.As(err, &x509.UnknownAuthorityError{}).
       On darwin, a nil RootCAs verification is delegated to Security.framework
       rather than Go's own chain builder, so the wrapped reason is a generic
       error string ("... certificate is not trusted"), not
       x509.UnknownAuthorityError — the test failed on every macOS dev machine
       while the connection was in fact correctly rejected. Fixed by asserting on
       the OS-independent wrapper crypto/tls.CertificateVerificationError instead
       (constructed identically on every platform in
       crypto/tls/handshake_client.go — verified against go1.27 stdlib source).

       PR review (#145) surfaced a second, sharper issue with that fix on its
       own: the broader type check alone can't tell "untrusted CA" apart from
       "wrong hostname" — and crypto/x509's platform-verifier branch (used on
       darwin whenever RootCAs is nil) folds hostname checking into the same
       opaque result as trust checking, so no error type can separate them
       there either (verified against the same source: the darwin branch in
       Certificate.Verify returns before Go's own VerifyHostname ever runs).
       Confirmed empirically: dialing the test server by its raw httptest IP
       instead of "localhost" produced the exact same wrapped error type as an
       untrusted CA. The test's own setup already tried to dial the certificate's
       actual "localhost" SAN via a string Replace on "127.0.0.1", but that
       silently no-ops on the IPv6 loopback net/http/httptest falls back to when
       IPv4 is unavailable — which would have let the test pass for the wrong
       reason without anyone noticing. Fixed by reconstructing the dial host with
       net.JoinHostPort (correct for both loopback forms, covered by
       TestDialNameFor_HandlesBothLoopbackForms) and adding a positive control:
       the same server + dial name, with the CA trusted, must succeed — which is
       what actually proves hostname verification isn't the failure mode, since
       the error type alone cannot on darwin. Behavior-only fix;
       internal/provisioning is unchanged. -->
- [x] FEAT-026: Diff & convergence engine (Normalizer mapping + Connector poll list reload) — *MQTT connector live reload landed under #119 (docs/adr/0008); other protocols still restart-only (see above).*
- [x] FEAT-027: Shared bidirectional resolver (`local_id`↔`point_id`, writeability/control schema lookup)

## Dependencies

- **Cross-repo:** `gutp-building-os-oss` #224 — gateway-scoped, versioned point-list provisioning API on Building OS. Until it lands, develop against a fixture snapshot file with the same schema.
- EP-003 (Normalizer) and EP-005 (control dispatch) consume the resolver.
