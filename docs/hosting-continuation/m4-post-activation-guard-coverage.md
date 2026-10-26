# M4 post-activation guard coverage

## Observed Windows failures

The [Windows core job on PR #233](https://github.com/tyhuang9/rig/actions/runs/40250689852/job/121341702825)
failed four generated-executor parents and three lineage children at head
`c4fe39ebaa82ae35752163da97f632e8f4ce54c8`, synthetic checkout
`57f55af24d93d061fb5d4024d9b46692db455f81`.
Raw SHA-256:
`edb36cddd91904680934bbb6d02a25092906ad9514035213a8795d3c65ee8ff1`.

Each reported a draining runtime with one route delegate, no passive attestation,
and no physical stop/removal. The tests expected one attestation call. Source
inspection shows the retry instead encounters a missing process-local retirement
handle and immediately pauses for route reconciliation. The fixture already
uses a one-millisecond drain period; this is not a drain timeout.

## Contract and scope

A passive route attestation cannot recreate retirement authority. An invocation
without the opaque live handle must remain closed. Later-worker and cross-process
completion remain separate E6 work.

The test-only correction preserves the five existing executor parents as
lost-handle refusal controls, including their phase, pause, durable state,
no-completion, no-republish and no-stop/removal assertions. Their unreachable
attestation counters reflect the actual conservative boundary. Those counters
do not establish route, claim or receipt-lineage validation.

Meaningful guard coverage uses the authentic existing `GeneratedRouteAuthority`
fixture and its read-only `AttestPublished` method:

| Parent | Cases |
| --- | --- |
| `TestGeneratedRouteAuthorityAttestPublishedAcceptsCurrentRouteAndRejectsPhysicalDrift` | Current claim succeeds; physical route drift refuses |
| `TestGeneratedRouteAuthorityAttestPublishedRejectsCurrentClaimAttemptDrift` | Future claim refuses; original claim against advanced current attempt refuses |
| `TestGeneratedRouteAuthorityAttestPublishedRejectsReceiptCreatorLineage` | Crossed creator job refuses; future creator attempt refuses |

Each case starts from genuine activation in a fresh isolated database. Receipt
mutation uses only the existing test seam, with the exact immutable trigger
restored and verified before attestation. Snapshots distinguish allowed reads
from SQL or physical effects. Typed errors identify the refusing guard.

A direct zero-creator-attempt case is excluded because the existing table CHECK
rejects that state. No schema constraint is disabled to fabricate it. The existing
executor zero-attempt case remains a lost-handle refusal control.

## Verification and remaining acceptance

The delivery record must include discovery and actual outcomes for the five
executor parents and three direct-authority parents, six direct cases, source
stability, scoped formatting/vet, canonical generation and documentation checks.
No production file, public API, timeout, recovery policy or fixture harness is
changed. Hosted Windows and Linux race behavior remain separate acceptance.

The Docker recovery pause `previous_retirement/retirement_unavailable` and the
retained gateway cold-start refusal remain open. This unit improves coverage and
corrects stale test expectations; it does not implement E6 or complete M4.

## Rollback

Revert the test and documentation commit. No runtime or schema rollback is needed.
