# Simulator integration (EP-009)

Wires the **sibling** standard-compliant simulators to the nexus connectors so
the BACnet and OPC-UA telemetry paths are exercised end-to-end against real
protocol stacks (no field hardware), terminating at the mock Building OS.

## Prerequisites

Check out the simulator repos **next to this one** (the compose build contexts
are `../bacnet-sim-gateway` / `../opcua-sim-gateway`, so they must be siblings of
the `nexus-gateway/` checkout):

```bash
# from the parent directory that already contains nexus-gateway/
git clone https://github.com/takashikasuya/bacnet-sim-gateway   # bbc-sim — BACnet/IP B-BC
git clone https://github.com/takashikasuya/opcua-sim-gateway    # opcua-sim — OPC UA server
```

If a sibling is missing, the profile's `up` fails with a build-context error
(`../opcua-sim-gateway: no such file or directory`) rather than anything
protocol-specific — clone the sibling and retry.

## Shared Point List

`point_list.csv` (the file the compose loads via `PROVISIONING_FILE`, mirrored by
`point_list.json`) is the single source of truth for native addressing. The 8
logical points `PT001..PT008` are modelled by **both** simulators with
protocol-native addresses, so the connectors and the Normalizer resolve
`local_id → point_id` against the same definition:

| point_id | BACnet (bbc-sim)        | OPC-UA (opcua-sim) | writable |
|----------|-------------------------|--------------------|----------|
| PT001    | `analogInput,1001`      | `ns=2;s=PT001`     | no       |
| PT002    | `analogInput,1`         | `ns=2;s=PT002`     | no       |
| PT003    | `binaryInput,1`         | `ns=2;s=PT003`     | no       |
| PT004    | `binaryOutput,2001`     | `ns=2;s=PT004`     | yes      |
| PT005    | `multiStateInput,1`     | `ns=2;s=PT005`     | no       |
| PT006    | `analogValue,1002`      | `ns=2;s=PT006`     | yes      |
| PT007    | `multiStateValue,3001`  | `ns=2;s=PT007`     | yes      |
| PT008    | `analogInput,1003`      | `ns=2;s=PT008`     | no       |

## Running

Profiles keep the two protocols separate so each `point_id` has a single source
per run:

```bash
# OPC-UA telemetry E2E (#39) — plain TCP, CI-friendly
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile opcua up

# BACnet telemetry E2E (#40) — needs host networking for Who-Is/I-Am broadcast
docker compose -f docker-compose.yml -f docker-compose.integration.yml --profile bacnet up
```

The gateway is overridden with `PROVISIONING_FILE=/fixtures/integration/point_list.csv`
(the file-backed Point List provisioning source; `.csv` here — see
`docker-compose.integration.yml`). This is a different mechanism from the base
stack's bootstrap fixture (`POINT_LIST_FILE=/fixtures/point_list.json`): when
`PROVISIONING_FILE` (or `PROVISIONING_URL`) is set it takes precedence, so the
integration run reads the CSV, not the JSON. The `point_list.json` alongside it is
a human-readable mirror of the same points and is not what the compose loads.

## Status

This slice provides the topology and shared addressing. Asserting telemetry
actually flows through to the mock Building OS is **#40 (BACnet)** and **#39
(OPC-UA)**; control round-trip is **#42**. Sibling-specific runtime details
(BACnet discovery params, OPC-UA endpoint/security) are finalized there against
a live run.

**Known limitation:** the connectors' poll lists (`BACNET_POINTS` / `OPCUA_POINTS`)
currently restate the native addresses that also live in `point_list.json`. The
two are kept in sync by hand here; deriving the connector poll list from the
shared Point List (so there is a single source) is connector-side work tracked
with the telemetry E2E slices (#39/#40).
