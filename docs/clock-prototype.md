# BACnet static clock prototype — Nexus #201, stages 1–2

This is an opt-in local prototype of the **Proposed** RI ADR-0009 and ADR-0010,
not an approved ontology/schema or a completed cross-repository rollout. Stage 1
used RI `6e0fefee8be2caaa33608d76845cbab2841cf9dd`; stage 2 pins the updated
observation contract to RI `9b643391d7273de4d92166d2474de1e5e0ea9f83`.

Reference paths at that commit:

- `docs/adr/0009-equipment-clock-source.md`
- `docs/adr/0010-simulation-observation-time-contract.md` (stage 2)
- `docs/architecture/oss-gateway-clock-provisioning.md`
- `docs/design/ri-606/fixtures/{provisioning.json,model.ttl,clock-shapes.ttl}`

`internal/provisioning/testdata/clock-provisioning.json` is an unchanged copy of
the synthetic JSON fixture, byte-identical at both SHAs. It contains 35 equipment points and a
separate shared clock; its addresses are test data, not a deployment map.

## Offline staging

The Core-side staging function `provisioning.StageClockSnapshot` validates the
entire snapshot (schema, content revision, explicit arrays, scope, native types,
units, references and conflicts) before returning any configuration. Unknown
fields, nulls, duplicate JSON keys, non-integer static numbers and unsupported
scopes fail. This stage supports one explicitly selected BACnet connector/device
per snapshot; it rejects mixed scopes rather than silently dropping them.

The CLI performs no network requests or live apply:

```sh
go run ./cmd/clock-config \
  --snapshot internal/provisioning/testdata/clock-provisioning.json \
  --gateway-id GW-EXAMPLE --connector-id bacnet-example --device-id 1234 \
  > /tmp/native-clock.json

# Separate canonical mapping for the existing Normalizer resolver:
go run ./cmd/clock-config \
  --snapshot internal/provisioning/testdata/clock-provisioning.json \
  --gateway-id GW-EXAMPLE --connector-id bacnet-example --device-id 1234 \
  --normalizer > /tmp/normalizer-points.json
```

The connector's new `BACNET_CLOCK_CONFIG_FILE` accepts the native output only,
with matching `CONNECTOR_ID` and `BACNET_DEVICE_ID`. It includes both native
equipment points and clock references from the same revision. Set
`BACNET_COV_ENABLED=false` and omit `BACNET_POINTS`; combining separate point
configuration with the bundle is rejected. Apply is startup/restart only.
Canonical point IDs and equipment membership stay outside the connector.
The bundle is a local staging artifact, not an authenticated distribution format;
keep it immutable and trusted. Its RI revision records provenance; the connector
cannot recompute the original snapshot hash from the native subset.

## Acquisition behavior

Clock readings bracket the whole equipment poll and every RPM chunk. Each
boundary also probes the configured object's `units` property; only BACnet
seconds (enum 73 or label `seconds`) are accepted. Missing objects, unreadable
properties, failed reads, invalid/nonfinite/negative values and incorrect units
do not produce a usable simulation time. Clock objects never become equipment
telemetry or command targets.

Equipment events retain their acquisition UTC timestamp and existing value
quality. Clocked polls preserve microseconds so the timestamp cannot fall before
the poll start through seconds truncation. The no-clock path retains its existing
format. Bound events carry string attributes:

| Attribute | Meaning |
| --- | --- |
| `sim_clock_id` | Opaque clock ID from the snapshot |
| `sim_config_revision` | Original static snapshot revision (the entire configuration, not a clock hash) |
| `sim_time_quality` | `stable_unfenced`, `changed`, `missing`, or `reset` |
| `sim_time_s` | Present only when every clock sample in the poll agrees |
| `sim_run_status` | `absent`: no run context is delivered or verified in this stage |

These attributes follow Proposed ADR-0010; they are not a formally approved
cross-repository telemetry contract. Acquisition quality and run validity are
independent. Stable seconds with absent context are diagnostic observations only,
excluded from run-based simulation-time joins/aggregation. `stable_unfenced` expressly
does **not** establish a run or exact simulation step: a reset and return to the
same value between reads cannot be detected. No `sim_run_id` or `sim_step_id` is
invented, and no fixed-step calculation is used. Backwards time latches `reset`
for that connector lifetime. Restart loses that history and still yields only
unfenced observations. No stale time is reused after missing/failed reads.
An unstable clock invalidates the association for all its bound values in that
poll; independent clocks are assessed separately. Successful equipment reads
survive partial equipment failures. Unbound points keep their existing behavior.
Uncertain associations emit warning logs with clock ID and quality.

The precedence is `reset > missing > changed > stable_unfenced`. New observations
emit only the canonical revision name; they never emit the old alias
`sim_clock_revision`. Existing events from Nexus `bc1a9e5` retain their original
alias and absence of run-status fields through normalization/storage/replay.
Interpreting those events as `absent` is the reader's compatibility view, selected
from trusted provenance; no producer rewrites or upgrades them. This changes the
attributes application contract only: gRPC services, Protobuf messages, field
numbers and `attributes`' existing `map<string,string>` type are unchanged.

## Validation and remaining stages

Python tests use injected BACnet responses and a recording publisher. Go tests
verify the pinned fixture, staged configuration, revision rejection, canonical
resolution in the Normalizer, and unchanged attributes/UTC after temporary
SQLite close/reopen and repeated reads. No Docker, REST, advance or physical
equipment operation is needed. Existing no-clock Python tests remain applicable.

Stage 2 vendors unchanged RI oracle/JSON fixtures in
`test/contracts/ri-clock-v2/`; `UPSTREAM.json` records the exact source SHA and
per-file SHA-256. Python tests run all pinned acceptance/rejection cases (including
legacy revision aliases and run-context examples). These are oracle baseline
checks, not proof of product run authentication, generation persistence or RI
Ingress implementation. No new Python dependency is required.

`TestClockContractPipeline` runs Core staging → actual Python connector with
injected BACnet responses → Go Normalizer → temporary SQLite close/reopen → RI
offline oracle. It records acquisition boundaries/samples in the test bridge,
separately from telemetry. It verifies 15 polls / 524 frames on each of two reads,
including fractional UTC, partial equipment failure, priority of uncertain states,
repeated polls, loss/recovery, reset latch and restart without run claims.
The ordinary Go replay test also covers unchanged old and new attributes.

Run from repository root after installing the existing BACnet test dependencies:

```sh
connector/bacnet/.venv/bin/python -m pytest connector/bacnet/tests -q
NEXUS_CLOCK_PYTHON="$PWD/connector/bacnet/.venv/bin/python" \
  go test ./internal/provisioning -run 'TestClockContractPipeline|TestClockAttributesSurviveNormalizerAndDurableReplay' -count=1 -v
```

The cross-language test uses the local BACnet venv by default and skips if it is
absent; explicitly setting `NEXUS_CLOCK_PYTHON` makes a missing interpreter fail.
The pinned oracle itself and its fixtures require only Python's standard library.

Not implemented in this stage: authenticated provisioning/v1 HTTP polling,
gateway live stop/drain/atomic apply/rollback with applied-revision ACK, multiple
connector/device scopes in one snapshot, run-context delivery/authentication/
generation persistence, strict step fencing, COV clock association, and RI
Ingress/storage/query/BDE cross-repository verification. The existing
file/url/fallback paths are unchanged; the draft snapshot is not fed to the old
Point List endpoint or merged with its deltas. Do not use separate generated
artifacts as evidence of atomic live application. Those integration stages
remain open under #201.
