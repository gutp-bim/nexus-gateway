# Run context without signatures (simulation scope)

Status: Proposed. Supersedes the Ed25519 signature requirement of ADR-0009 only.
Related: #201, RI #613, BDE #773 / ADR-0016.

## Decision

The testbed is a simulation. The threat model is accidental run mixing (stale run,
generation rollback, expired context, wrong gateway/clock), not a forged run_id.
A caller able to forge a run_id is accepted as out of scope. Therefore the run
context payload no longer needs an Ed25519 signature, a configured public key, or
the BouncyCastle dependency on the RI side.

## Kept from ADR-0009 (unchanged)

- Explicit (gatewayId, clockId) scope from trusted operator configuration.
- Durable ledger: Initialize/Open separation, integer generation 1..2^53-1,
  rollback rejection, idempotent same-generation/body, Commit before Apply.
- Whole-poll-interval check, UTC lifetime, provisioning revision match.
- Reset/InvalidateRun persistence; revoked run IDs stay invalid.
- Opt-in only; absent otherwise. Old consumers unaffected.
- mTLS to RI, authorization and Safety/BOS-write boundaries are NOT relaxed.
  run_id is an annotation, never an authentication or authorization input.

## Removed

- Envelope signature, ledger-bound public key, key rotation procedure.
- Any claim that the context proves who issued it. The payload is trusted only
  because the operator placed the file; file permissions are the only control.

## RI side

RI fails closed: a run claim received while no run-context trust configuration
(expected scope/run_id) exists is rejected, not passed through. Validity attributes
are published only when scope, run_id and generation match configuration.

## Compatibility and rollback

Payload schema (`clock-run-context/2-draft`) is unchanged; only the envelope loses
`signature`. The signed variant remains in git history (PR207 571dc02, PR613 936a787).
Rollback: do not configure --clock-run-context. Do not reuse a ledger across
the signed and unsigned formats; initialize a new authority domain.

## Verification scope

Go unit tests only until live Nexus -> RI -> BDE integration is run. No claim of
forgery resistance is made; tests must not assert it.
