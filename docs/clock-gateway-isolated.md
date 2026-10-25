# Managed clock provisioning: Gateway wiring and isolated runbook

Nexus #201, starting at `e8fa5dfd269dc8faf16e24675ebecda589a43890`.
RI ADR-0009/0010 remain Proposed. This adds concrete Gateway adapters to the
[fetch/apply controller](clock-provisioning-apply.md); it does not introduce run
contexts or change gRPC definitions. All clock observations retain
`sim_run_status=absent`.

## Opt-in and exclusive ownership

`--clock-provisioning` (`CLOCK_PROVISIONING=true`) selects a **dedicated single
BACnet Gateway**. It requires URL-only provisioning, a gateway mTLS certificate,
explicit native device ID, persistent absolute state directory and connector
spec. `file`, `fallback`, simultaneous `--provisioning-file`, dev-sim and catalog
installation/updater are rejected in this mode. Without the flag, all previous
file/url/fallback, normalizer, lifecycle and command behavior is preserved.

The managed branch does not start `pointsync.Loop`, load a legacy fixture or
start the old `evt.>` Normalizer/Pump. It owns one durable JetStream consumer on
`evt.bacnet.<connectorID>` and writes directly to the existing SQLite buffer.
Normal Building OS uplink and Egress remain active. The dispatcher gates each
whole command, including reverse lookup and its reply, before a mapping change.
The opt-in branch disables MQTT subscription-sync routes; no second mapping
writer runs alongside the managed controller.

Lifecycle Claim reserves the connector until Gateway termination. Admin
start/stop/restart/upgrade/rollback, catalog install/update and Watch restart
cannot bypass it, including during a halted recovery. Image pulling is not part
of the managed transaction: the operator supplies an already loaded,
digest-pinned, verified image. An existing container is adopted for termination
only if its `nexus.clock.connector` label matches the connector. An unowned
same-name container causes an error, not an automatic stop/remove.

Only one Core process may own this isolated bus/connector; stop the old Core
before switching modes. Reservations coordinate management paths in this Core,
not a distributed election with another Gateway process. Do not run an old
Gateway/legacy producer alongside this mode. Unbound old-format events are
quarantined rather than guessed into the new mapping.

## Concrete transition

1. Close Core telemetry admission and command admission; await active writes and
   durable event work. Tell a running managed producer to quiesce.
2. Python closes command/poll admission, waits on the poll/write lock, completes
   the current poll's awaited JetStream PUBACKs and flushes NATS before replying.
3. Stop/inspect/remove the owned container. Keep its identity even after partial
   start/remove failures. A crash-left labeled instance is stopped on cold Hold
   before the first RI fetch. No new producer is started if termination cannot
   be confirmed.
4. Drain the dedicated consumer under a deadline. Completion requires zero
   broker pending/ACK-pending work and zero active processing. Successful frames
   get SQLite durability followed by broker-confirmed `DoubleAck`. RI uplink
   delivery is not part of this barrier.
5. Persist a fresh random process session, its validated full snapshot and a
   native-only bundle, using fsync/atomic rename before configuring/starting.
   Then replace the current control/Normalizer mapping behind the gate.
6. Python starts **paused**, parses the bundle and reports capability
   `clock-runtime/1`, process session, configuration revision, SHA-256 of the
   actual file it parsed and paused state over
   `cfg.bacnet.<connectorID>.runtime`. Core checks all fields before recording a
   verified-session receipt. A generic health boolean is insufficient.
7. Resume that exact session/revision/hash. Persist its active recovery pointer
   and release Core telemetry/control admission only after the response.

The private runtime subject must permit requests only from the owning Core by
the lab's NATS ACLs. The managed connector subscribes and responds, and alone
publishes its event subject. Runtime
session IDs are process identity, **not** a simulation run ID or time fence.

## Separate NATS identities (#202)

Managed specs require their own `NATS_URL` (explicit scheme/host, no userinfo,
query or fragment) and absolute `NATS_AUTH_FILE`. Core never substitutes its
`--nats` URL. Only the authentication file path appears in specs, status and
container environment; Core binds that single existing regular file readonly
at the same absolute path. The producer reads a JSON object with exactly
`user` and `password`, both nonempty strings. Supply it through the lab's secret
delivery mechanism, outside git and the session/native journal. Set ownership
and mode so only the operator/Core and connector UID 10001 can read it (for
example, a connector-owned 0400 file delivered by a privileged operator).
Mount it at the same path in a containerized Core, too; do not mount a directory
of Core secrets into the producer. Core's identity is configured independently
through its existing connection configuration. TLS URLs use each runtime's
normal certificate verification; never disable it to fix authentication.

Missing/malformed files or rejected credentials keep producer admission closed;
they never fall back to Core's identity. Update a secret by stopping the managed
Gateway and replacing the file through that same delivery mechanism, then
restart; preserve the session journal. Existing file mounts retain their inode,
so replacing a host file is not a live credential rotation protocol. Managed
authentication is opt-in only; legacy connector behavior is unchanged.

For connector `bacnet-boptest`, configure the following explicit broker policy
using different Core/producer users and secrets:

| Identity | Publish | Subscribe |
| --- | --- | --- |
| Core | `$JS.API.>`, `$JS.ACK.>`, `cfg.bacnet.bacnet-boptest.runtime`, `cmd.bacnet.bacnet-boptest` | `_INBOX.core.>` |
| Producer | `evt.bacnet.bacnet-boptest` | `cfg.bacnet.bacnet-boptest.runtime`, `cmd.bacnet.bacnet-boptest`, `_INBOX.bacnet-boptest.>` |

Producer permissions additionally require NATS `allow_responses` with one
response per received request and a bounded expiry (the contract test uses
one minute). This permits runtime/control replies to Core's generated inboxes
without granting unrestricted inbox publication. Its own inbox subscription
receives JetStream PubAcks. No producer `$JS.API` permission is needed: managed
Core creates EVENTS before starting the child, so managed Python skips legacy
stream discovery. Core cannot directly publish producer events; producer cannot
initiate runtime commands, JetStream administration or unsolicited Core inbox
messages. Scope the bus/account to this one Gateway, as required above.

Authenticated embedded-broker tests run the actual Python startup path with
injected equipment, verify command replies/PubAcks, assert broker permission
violations for forbidden operations, and cover apply, partial-start rollback,
journal recovery and missing/malformed/wrong credentials. Synthetic secrets are
also checked against the generated spec/environment, status and failure logs.

## Delayed delivery and recovery

Every managed event, including clockless events, carries transport headers
`Nexus-Session` and `Nexus-Config-Revision`. The worker validates these against
the durable verified-session archive and resolves native IDs using **that
session's immutable snapshot**, never today's resolver. Old events can therefore
arrive after switching, rollback or Gateway restart without acquiring new point
IDs, unit labels, timestamps or run metadata. Delivery remains at-least-once;
an ACK failure may duplicate an unchanged frame, not relabel it.

Unknown, unverified, malformed or mismatched-session events are durably stored
under `quarantine/` before terminating their consumer delivery. Raw EVENTS data
is not deleted. Quarantine includes raw bytes/headers and stream sequence;
warning logs contain reason/sequence, not the payload. An unverified failed
candidate cannot be drained using an assumed candidate mapping. Archived
sessions are not garbage-collected automatically: deleting one could invalidate
late replay.

Normal apply failures stop/drain the candidate and restore the full last-good
snapshot through a new paused session and a fresh runtime check. Restoration
failure keeps the gates closed and attempts producer termination; applied
revision is unknown. Admin/Watch cannot restart around that state. The previous
snapshot stays available for retry, and a 304 does not acknowledge a failed
apply. A saved active session restores only a rollback candidate on Gateway
restart, never a fabricated running-applied claim; RI is fetched and the full
transition verified again.

The state directory contains private `sessions/*.json`, their verified receipts,
native-only readonly files, `active.json` and quarantine records. Core owns the
directory with mode 0700. Only `native/` is mounted readonly into the
unprivileged connector; native files are 0444, and no canonical point IDs or full
snapshot are mounted. Use the **same absolute path on the daemon host and in the
Gateway**; do not mount this under an inaccessible `/root` path in the connector.
Keep the directory across restarts and scope it to exactly one Gateway/device.

`GET /clock-provisioning` exposes desired/applied/last-good/phase/error using the
existing Admin viewer authorization. The old Egress `applied_revision` field
still means a point-only API ETag; the new full snapshot hash is not mislabeled
as that ETag. A new RI apply-ACK wire contract is not invented here.

## Safe isolated verification: no Docker or equipment required

With the existing BACnet test dependencies installed, run from repository root:

```sh
NEXUS_CLOCK_PYTHON="$PWD/connector/bacnet/.venv/bin/python" \
  go test ./internal/clocksync -run TestGateway -count=1 -v
connector/bacnet/.venv/bin/python -m pytest connector/bacnet/tests -q
```

`TestGatewayActualPythonProducerAndRecoveryJournal` starts an ephemeral mTLS RI
mock and embedded NATS, runs actual Python Config/Connector/ManagedRuntime as
subprocesses behind a fake Docker Engine, injects all BACnet responses, and uses
real session files, Normalizer and temporary SQLite. The harness never imports
or constructs a BACpypes3 application or opens equipment sockets. The RI input
is the unchanged pinned Builder→RI snapshot; test-only changes exercise ID/clock
transitions. The test stops/restarts the producer and restores the journal.
Other Gateway tests cover delayed old sessions after mapping change, quarantine,
in-flight command completion, bad receipts, partial starts, failed restoration
and management exclusion. Explicit interpreter selection makes a missing Python
environment fail instead of skip. Test cleanup stops its subprocesses and NATS.

## Isolated binary/container startup (operator procedure, not executed here)

Use a new lab bus/volume, a dedicated **internal** Docker network, mTLS RI mock,
TLS BOS mock and test certificates. Do not share a production NATS bus, Docker
connector name, network interface or equipment endpoint. Both the Gateway and
child containers must resolve the same lab NATS URL. Configure NATS ACLs for the
event and runtime/control subjects. Keep the existing Admin JWT verification
when exposing its API.

An isolated child image can overlay the injected-only harness using
`connector/bacnet/Dockerfile.managed-test` and a preloaded verified connector base
image specified by digest (`CONNECTOR_BASE`). Its entrypoint uses
`managed_process.py`, not the production BACnet client. Build/verify/preload this
image in the lab before activation; no image build/pull was executed by the
agent. Substitute the resulting digest into a local spec:

```json
{
  "ID": "bacnet-boptest",
  "Image": "registry.example.com/managed-test@sha256:<verified-64-hex-digest>",
  "Env": [
    "BACNET_ADDRESS=192.0.2.1",
    "BACNET_POLL_INTERVAL=60",
    "NATS_URL=nats://nats-lab:4222",
    "NATS_AUTH_FILE=/run/nexus-secrets/producer-auth.json"
  ]
}
```

The harness asserts that address and never contacts it. Keep native points,
clock file, process session and COV flags out of the spec; Core derives them.
Example Gateway flags (all endpoints/certificates must belong to that lab):

```sh
gateway --clock-provisioning \
  --gateway-id GW-BOPTEST-001 --clock-device-id 599 \
  --clock-connector-spec /lab/connector.json --clock-state-dir /srv/nexus-clock \
  --provisioning-mode url --provisioning-url "$LAB_RI_HTTPS" \
  --provisioning-ca /lab/ca.pem --provisioning-cert /lab/gateway.pem \
  --provisioning-key /lab/gateway-key.pem \
  --nats "$LAB_NATS_URL" --connector-network nexus-clock-lab \
  --bos "$LAB_BOS_TLS_ADDR" --bos-ca /lab/ca.pem \
  --bos-cert /lab/gateway.pem --bos-key /lab/gateway-key.pem \
  --admin-jwks-url "$LAB_JWKS_URL" --admin-addr 127.0.0.1:18080
```

For a containerized Gateway, bind `/srv/nexus-clock` at that same path and permit
its existing daemon client to use the lab daemon socket. Mount lab certificates
readonly. Do not add trusted identity headers, TLS bypass or authentication
disable flags. Do not also start the legacy BACnet service: Core owns the child.

## Stop, failed-apply restoration and rollback procedure

1. Inspect authenticated `/clock-provisioning`. `active` requires matching
   desired/applied. `retry_pending` means the old snapshot is running and the
   desired candidate is not acknowledged. `halted` means admission stays closed,
   not that an uncertain daemon termination succeeded.
2. To test failed apply, change only the lab RI mock snapshot or inject a fake
   Engine/receipt fault. The automated tests above do this without a live RI
   import. Check full restoration of the old native/mapping revision; management
   restart/update must remain refused. Fix the lab cause and let a later poll
   retry the staged candidate, including a 304 response.
3. For shutdown send SIGTERM to the **Gateway**, not an Admin restart to its
   owned child. Core closes admission, cancels/joins apply, refuses further
   producer activation, stops/drains the owned child and then ends the worker.
   Give the lab Gateway a stop grace period of at least 60 seconds with the
   default 15-second operation timeout; increase grace if that timeout changes.
   Inspect failures rather than treating a forced kill as a confirmed drain.
4. Preserve the state volume. Restart only one Gateway with the same scope/path.
   It stops any crash-left **labeled** child, loads validated archive/last-good
   recovery data and refetches RI. Missing/unauthorized RI or a corrupt journal
   cannot silently open admission. Do not edit native/session/verified files or
   reuse a session ID to clear a fault.
5. To roll back static configuration, stop the isolated run, serve the previous
   complete snapshot from the lab RI mock, then restart with the persistent
   archive. Old observations retain their original session/revision. To revert
   to legacy mode, first stop the managed run and confirm its child terminated;
   then run the old binary/config on its original dedicated lab bus. Do not
   delete the new archive or repurpose its raw events for a legacy resolver.

No real equipment, BOPTEST REST/advance, running RI, migrations, deployment,
other-repository modification, push or merge is part of the executed validation.
