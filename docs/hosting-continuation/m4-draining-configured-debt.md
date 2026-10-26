# Interrupted Draining capacity recovery

Status: focused local tests and static checks passed, based on draft PR #251 at
`5bd5bf49e6cc1d41009eb6f7aeb81060bbed4774`. Hosted acceptance remains pending.

## Purpose

Reconstruct the successor's configured capacity debt after a process stops between
the completed accounting handoff and deployment completion. The authentic
boundary has candidate receipts, activation, complete predecessor retirement and
both accounting handoff records (041–044), but no completion receipt (045).

The deployment remains `Draining`. Recovery must retain the successor's configured
resource obligations without a second raw capacity acquisition. This unit does
not restore permission to complete a deployment or to repeat retirement effects.
Later-worker completion and interruption before accounting handoff remain separate
requirements in the existing E6 recovery work.

## Implementation

The typed startup snapshot now reads predecessor retirement observations and the
aggregate retirement receipt independently in its existing transaction. Each
projection is validated and deterministically ordered. Empty record sets remain
distinguishable from a complete handoff.

The cold recovery consumer correlates those records with activation, component
rosters and both accounting handoff records. An exact completed handoff adds the
successor's configured limits to the existing atomic bootstrap debt batch. The
group remains classified as `Risky`; only its accounting source changes. After
the batch is installed, the existing pending-publication path publishes the debt
tokens. That group bypasses a second raw reservation.

Valid earlier handoff prefixes continue through the existing raw-reservation
path. Crossed or inconsistent relevant evidence refuses construction. The shared
capacity coordinator, ownership tokens, registry publication and abort behavior
are reused.

## Required invariants

- Match complete application, deployment, component, generation, job and attempt
  identity across immutable evidence and the current runtime head.
- Require complete successor physical/configuration proofs and fresh predecessor
  absence, including the existing V1/V2 retained proofs and final reattestation.
- Never restore debt from missing, partial, crossed, duplicate or orphaned
  evidence or a 045 record for the interrupted successor. Preserve the
  predecessor's completed history. Legitimate earlier interrupted states retain
  their existing `Risky` recovery behavior.
- Publish one complete policy-derived successor debt batch through the existing
  private ownership and registry transaction. Active but unhealthy containers
  remain charged.
- Keep `Draining` and the successor's completion-receipt absence unchanged. Leave operations
  and aliases must not release debt. Persisted records do not create completion,
  claim, pinned execution, stop/remove or predecessor-release authority.

## Executed verification

The authentic regression uses the real sequential activation, retirement and
accounting handoff APIs, then discards the old capabilities and uses fresh cold
construction with insufficient spare headroom. On unchanged production it reached
`stage=cold_construction reason=cold_reservation` and failed in 1.25 seconds.
Fresh compilation, exact discovery and the cheap baseline control passed. Source
hashes before and after execution matched.

Two earlier fixture failures remain recorded: the first omitted `Retire` before
handoff; the second expected a `Draining` component instead of the producer's
`Active` component within a `Draining` deployment. Only the corrected third run
demonstrates the capacity refusal. No persisted state was edited to obtain it.
The repaired authentic parent passed in 1.47 seconds, including both nested
process-isolation layers. It compares complete SQL and protected-state snapshots,
checks the exact published debt token, and observes zero fresh capacity probes or
repeated stop/remove effects. The deployment remains `Draining` with its successor
component `Active` and no successor completion receipt. This local regression uses
deterministic container observations; real Docker acceptance remains hosted.

The accepted union contains **34 distinct passing names: nine parents and 25
children**. Nested process output produces 38 raw PASS records. Earlier failed
attempts are excluded from that union.

| Check | Result |
| --- | --- |
| Four runtime-state parents | Empty history, complete chain, independent sorted 043 projection, and malformed/crossed state passed |
| Three cheap ingress parents | Existing exact debt release, completed Current evidence, and Draining evidence/prefix/refusal controls passed |
| Authentic interrupted Draining | Passed in 1.47 seconds; complete SQL/protected equality, exact debt ownership and no new acquisition or predecessor effects |
| Completed Current compatibility | Distinct replacement passed in 1.92 seconds |
| Fresh final package compilation and discovery | Both binaries compiled; exactly four runtime and five ingress parents discovered |
| `gofmt -d` on the five changed Go files | Empty output |
| `go vet ./internal/generatedruntimestate ./internal/generatedingress` | Passed |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed |
| `git diff --check` and before/after source hashes | Passed; LF/CRLF notices only |

Behavior runs used the pinned Go 1.26.7 toolchain with `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`, exact anchored test
selectors and `-test.count=1`. Each expected name was required by discovery.

The controlling source freeze is `source-freeze-v4.json`, SHA-256
`d23a6b4ba70edd4cbb6fad9d8a092e65f9fc9b97bd1c92291f33d90ce4bc7fa6`.
The final compile/static receipt is SHA-256
`8664e1ad67f8db62af8b5b0dbde10f8940af6228ceaa6adddb7849b76ac3c4db`.
The final ingress binary is SHA-256
`10eca41660b46e34c9c7c1b620ae34ad7a87267f5c8e9eb0238eaf66b7a95a91`;
the runtime-state binary is
`91688e46c61403c4fb500f84772fa0360dda4a93813a2348a7b803eb91e1ecc0`.
The qualified original-failure receipt is
`977c13f87e0dfef344669d2734284a5a0e469d8eb59641956212cf2a40a16788`.

These records and versioned behavior logs are in the task's
`temp/m3-lan-external-db-20261015/m4-lifecycle-coordinator-verification/pr222-ci-repair-20261023T2125Z/pr251-e6-draining-configured-debt-{red,green}-20261026`
directories. During repair verification, new fixture attempts failed on an active
job uniqueness constraint, an incomplete first completion setup, and missing
release/slot pins in the pure evidence fixture. They were corrected without
relaxing production checks. Production bytes remained unchanged after the first
green-source freeze.

The early compile/discovery runner reused v1 log filenames for v2/v3; those raw
logs cannot be claimed as separately preserved. Final compilation and discovery
were repeated into a fresh exclusive `final-v4` directory. Its receipts and binary
hashes provide the controlling build evidence. The separately named behavior
failure logs remain retained.

## Remaining acceptance and rollback

Fresh hosted Docker and Linux race checks remain necessary for the changed cold
path. Historical PR #247 A0 acceptance and PR #246 gateway results provide their
own revision-specific evidence and do not establish this interrupted state.

Requirements come from `m4-generated-candidate-authority.md` and
`m4-accounting-completion.md`. The original user-supplied Downloads documents are
unavailable at their recorded paths during this continuation.

Rollback is a normal revert of this cohesive change followed by startup
verification. Keep immutable receipts and protected history intact; clearing debt
or deleting evidence to force startup would violate the recovery contract.
