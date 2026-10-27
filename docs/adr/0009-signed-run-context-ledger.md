# Signed run context: durable receiver ledger

Status: Proposed. Related: #201, RI ADR-0010, BDE #773 / ADR-0016, Testbed #2.

## Contract proposal

Use the existing RI `clock-run-context/2-draft` payload. A Testbed-owned issuer
signs its exact UTF-8 JSON bytes with Ed25519; an envelope carries base64 payload
and signature bytes. The receiver is configured with a public key and one explicit
(gatewayId, clockId) scope through trusted operator configuration. An embedded key
or a payload flag is never evidence of authentication or authority.

No REST endpoint, protobuf change, connector-held signing key, or BOS write
credential is added. Key authorization is constrained to the configured scope.
Issuer key rotation and recovery after losing trusted history require a separate
operator procedure, not automatic first-key/first-generation trust.

The new `internal/runcontext` module is a prerequisite only: it is NOT wired into
Gateway, connector, RI ingress or Testbed launch. All current clock telemetry still
reports `absent`. No production authentication behavior changes in this slice.

## State and acquisition

Initialize creates an explicitly new authority domain with generation zero. It
refuses an existing file; the first committed context must have generation one.
Open requires an existing, recoverable scope/key-bound ledger. Never call Initialize
as a fallback after a missing/corrupt ledger. The caller must exclusively own the
ledger throughout its lifetime; this module does not implement a process election.

Verify signature, schema, scope, UTC lifetime and integer generation (1..2^53-1).
Commit authenticates and durably records the context before Apply records its
separate activation boundary. Same generation/body is idempotent across JSON key
ordering, a changed body or generation rollback is rejected. A higher generation
clears the apply receipt until explicitly applied again. No inference of physical
simulator running state is made.

Evaluate checks the entire acquisition interval: apply <= poll start,
validFrom <= start <= end < expiresAt, matching provisioning revision and active
context. The result is valid plus runId/generation, or absent/invalid/unverified
without run claims. A receiver persistence failure poisons the live ledger until
recovery, preventing an old in-memory lease from remaining valid.

A reset must durably InvalidateRun before subsequent telemetry admission.
Invalidated/revoked run IDs remain invalid across restart and higher generations.
Recovery requires a new run ID with a higher generation and fresh Apply.

The caller must serialize apply/reset/poll admission, bind the lease to an archived
managed session, capture full poll boundaries, and archive the acquisition result
before buffering. Delayed replay must use that archived result and original lease,
not Evaluate against the current lease. These caller responsibilities are NOT yet
implemented by this module; unit tests are not proof of runtime delivery/recovery.

## Compatibility and remaining work

Old producer/old consumer behavior is unchanged (`absent` for current managed
Nexus). A future opt-in verified producer emits RI's `valid`, not BDE's legacy
`running`. BDE must explicitly pin `expected_run_id`; old BDE rejects `valid`.
Missing/invalid/unverified context remains diagnostic telemetry and is never
promoted to a valid run. `stable_unfenced` is independent of context validation;
no strict step fence or sim_step_id is claimed.

Pending: trusted issuer provisioning/rotation, explicit CLI delivery and recovery,
managed session/poll binding, historical receipts, RI ingress trust/validation,
full producer-consumer contracts and negative integration tests. Restore of a
complete older trusted filesystem snapshot is outside this local ledger's rollback
protection; an external trusted floor/checkpoint is required before such recovery.

Rollback: do not configure the future opt-in integration. Existing runtime is
unchanged. Do not delete a ledger and initialize it to bypass generation history.
