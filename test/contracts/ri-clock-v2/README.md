# Proposed RI ADR-0010 offline contract fixtures

Source: `gutp-bim/gutp-building-os-ri`, commit
`9b643391d7273de4d92166d2474de1e5e0ea9f83`,
`docs/design/ri-606/time_contract.py` and `docs/design/ri-606/fixtures/*.json`
(including `fixtures/compat/`). These upstream files are copied unchanged.
`UPSTREAM.json` lists their SHA-256 hashes; the BACnet contract test verifies them.

The oracle implements the Proposed contract for offline tests only. It is not an
RI ingress handler, authenticator, run registry, or evidence of a completed
cross-repository rollout. Context/receiver evidence is test input, not a trust
mechanism. Compatibility profiles are selected by the test's known source
provenance and never from an event's self-assertion.

`connector/bacnet/tests/test_clock_contract.py` runs the upstream cases without
new dependencies. `internal/provisioning/clock_contract_test.go` additionally
tests real connector output through Normalizer and durable replay against this
oracle using independently recorded test acquisition evidence. No fixture
evidence fields are sent as telemetry attributes.

Only the JSON/time-contract subset is vendored here; RDF/SHACL validation and
the upstream full verifier are outside this test. No RI service or live equipment
is contacted.
