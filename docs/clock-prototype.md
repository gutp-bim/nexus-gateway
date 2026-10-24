# BACnet static clock prototype — Nexus #201, stage 1

This is an opt-in local prototype of the **Proposed** RI ADR-0009, not an
approved ontology/schema or a completed cross-repository rollout. Reference RI
commit: `6e0fefee8be2caaa33608d76845cbab2841cf9dd`.

Reference paths at that commit:

- `docs/adr/0009-equipment-clock-source.md`
- `docs/architecture/oss-gateway-clock-provisioning.md`
- `docs/design/ri-606/fixtures/{provisioning.json,model.ttl,clock-shapes.ttl}`

`internal/provisioning/testdata/clock-provisioning.json` is an unchanged copy of
the synthetic JSON fixture at that SHA. It contains 35 equipment points and a
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
quality. Bound events carry string attributes:

| Attribute | Meaning |
| --- | --- |
| `sim_clock_id` | Opaque clock ID from the snapshot |
| `sim_clock_revision` | Original static snapshot revision |
| `sim_time_quality` | `stable_unfenced`, `changed`, `missing`, or `reset` |
| `sim_time_s` | Present only when every clock sample in the poll agrees |

These quality labels and the revision attribute are local prototype vocabulary,
not an approved cross-repository telemetry contract. `stable_unfenced` expressly
does **not** establish a run or exact simulation step: a reset and return to the
same value between reads cannot be detected. No `sim_run_id` or `sim_step_id` is
invented, and no fixed-step calculation is used. Backwards time latches `reset`
for that connector lifetime. Restart loses that history and still yields only
unfenced observations. No stale time is reused after missing/failed reads.
An unstable clock invalidates the association for all its bound values in that
poll; independent clocks are assessed separately. Successful equipment reads
survive partial equipment failures. Unbound points keep their existing behavior.
Uncertain associations emit warning logs with clock ID and quality.

## Validation and remaining stages

Python tests use injected BACnet responses and a recording publisher. Go tests
verify the pinned fixture, staged configuration, revision rejection, canonical
resolution in the Normalizer, and unchanged attributes/UTC after temporary
SQLite close/reopen and repeated reads. No Docker, REST, advance or physical
equipment operation is needed. Existing no-clock Python tests remain applicable.

Not implemented in this stage: authenticated provisioning/v1 HTTP polling,
gateway live stop/drain/atomic apply/rollback with applied-revision ACK, multiple
connector/device scopes in one snapshot, run-context delivery/authentication/
generation persistence, strict step fencing, COV clock association, and RI
Ingress/storage/query/BDE cross-repository verification. The existing
file/url/fallback paths are unchanged; the draft snapshot is not fed to the old
Point List endpoint or merged with its deltas. Do not use separate generated
artifacts as evidence of atomic live application. Those integration stages
remain open under #201.
