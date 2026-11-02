# Builder → RI generated snapshot: Nexus offline integration input

- Nexus baseline: `6c7d3bf684373b13fc4ebfc5baac0e5f854fefdf`
- RI source: `9083af2f851a38a442f90420e3cef913081c66a7`
- Builder source: `f2f313e044b85f7ef127ef95351afef81afdcbc9`
- ADR-0010 offline oracle: RI `9b643391d7273de4d92166d2474de1e5e0ea9f83`
  (unchanged at the RI source SHA above).

`provisioning.json` is an unchanged byte copy of the user-supplied
`artifacts/ri-606-9083af2/provisioning.json`. SHA-256:
`b725e01075fbcfcf672e0a6f7021491d96ee584ce49de9bd8de12c0338d8e41e`.
It is recorded as the output of RI's real import/materializer, source/DTO and
direct controller invocation, not a handwritten snapshot. The supplied
`results.md` and `results.json` were inspected; RI was not rerun in this task.
They do not establish HTTP route/middleware/authentication or live telemetry.

`clocks.json` is an unchanged copy of the Builder clock sidecar.
`points.csv` projects only the native addressing, canonical ID, protocol, gateway,
unit and writability columns from `artifacts/builder-44-f2f313e/pointlist-v2.csv`.
Field values, ordering and empty units are unchanged. This independent expected
data is not synthesized from the RI snapshot or Nexus staging output. The full
CSV/RDF remain in the supplied artifacts. `PROVENANCE.json` records source SHAs,
hashes for those originals and the supplied reports, and hashes for these copies.
The original CSV artifact uses CRLF; its LF-normalized hash matches the RI
report's stored CSV hash. The expected-value projection uses LF.

`TestClockContractPipeline/builder-ri` reuses the existing synthetic pipeline:

1. Verify snapshot bytes and compare all 35 points with the CSV projection.
2. Derive native connector configuration with `StageClockSnapshot`.
3. Run actual Python connector code with injected BACnet responses and publisher.
4. Resolve each native address to its original BPT-0002…BPT-0036 in Go Normalizer.
5. Persist, close/reopen temporary SQLite and read twice with a changed current
   resolver. Values, IDs, attributes and acquisition UTC must remain unchanged.
6. Validate each replayed frame against the same real snapshot using the pinned
   Proposed ADR-0010 offline oracle and separately recorded acquisition evidence.

The input has 35 equipment points, one clock and one equipment binding. Scope is
GW-BOPTEST-001 / bacnet-boptest / device 599, with internal analogValue,2 obtained
from configuration. There are 31 read-only and four writable equipment points.
Four points omit `unit` in the snapshot; the connector carries their empty unit
and the Normalizer omits the `unit` attribute. Other units are preserved exactly.
No expectation uses the synthetic fixture's device 1234 or constant unit `1`.

The 15 polls / 524 emitted frames cover stable, changed, missing and reset clocks,
partial equipment failure, repeated polls, recovery and restart without run
claims. Each native point gets a distinct injected numeric value so address/value
swaps can be detected. The real Python write gate also runs exclusively against
an injected recorder: it allows only the four configured writable equipment
points and rejects the clock. No BACnet stack or real WriteProperty is opened.
Canonical IDs never enter the connector bundle or its Common Events. No run ID,
step ID or `valid` status is generated.

Run from the repository root with the existing BACnet test venv:

```sh
NEXUS_CLOCK_PYTHON="$PWD/connector/bacnet/.venv/bin/python" \
  go test ./internal/provisioning \
  -run 'TestBuilderClockSnapshotProvenance|TestClockContractPipeline' -count=1 -v
```

The CSV/provenance test runs even without Python. Set the interpreter explicitly
to require the cross-language test instead of permitting a missing-venv skip.
This verifies mock acquisition through local storage and the contract oracle;
it does not verify physical BACnet, BOPTEST REST/advance, RI Ingress/query, runtime
configuration apply, run-context delivery or authentication. No Docker, other
repository writes, push or merge is needed.
