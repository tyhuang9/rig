# Restored configured capacity accounting

Status: implementation and focused local verification complete. Hosted Docker
and Linux race verification of this change, and full recovery acceptance, remain
pending.

## Scope

Restore the configured capacity charge for a completed current workload after a
controller restart, using its exact durable completion evidence and current
physical attestation. A running workload already consumes physical resources;
restoring its configured charge must not require another ordinary allocation.
New admission must account for both restored charges and live allocations.

The implementation is limited to complete post-044/045 `PhaseSucceeded` Current
workloads. It preserves explicit compatibility for legacy histories with all
044/045 evidence absent. Partial, crossed or unresolved evidence must refuse.
Interrupted activation and draining, later-worker authority reconstruction, and
the remaining E6 recovery cases are outside this unit.

## Hosted prerequisite accepted

[PR #247's A0 job](https://github.com/tyhuang9/rig/actions/runs/40309975223/job/121528400941)
passed on 2026-10-26 at synthetic checkout
`ab347ec118fa0c7bcc9ee5cecfba00b461cf8338`, matching the source tree of published
head `3a57e8d81afaf683144f21d0e74fd26da5ec0dd9`.

The workflow ran the exact
`TestLiveControllerGatewayRebindCrashMatrix/boundary/a0-sql-claim` case. Its
407.88-second pass follows initial normal admission, a proven durable SQL-claim
crash, kill/reap, fresh recovery-only startup and committed terminal/current
proof, recovery-process reap, a third fresh normal process, immutable terminal
replay, both application serving probes, and final reap. Independent fixture
cleanup also passed.

The preceding manifest and runner controls passed 591 distinct test names across
25 parents. The live parent and selected A0 case each ran and passed exactly
once. Neither stream contains a failed or skipped test. The workflow enforces
these results and package completion with `pipefail` and exact JSON checks.

| Evidence | SHA-256 |
| --- | --- |
| Hosted raw job log | `94d000f5f3fe6fcfd17c537f633291cdcb8778a6d2371a4a1d49556f046da774` |
| Root acceptance audit | `5b53caf39536202b99ad805f6cf8ee3f875e48bf2f7a546611d81c8cc1bc9e78` |
| Independent prerequisite review | `d3db1658eb8531cb7c078987e59f740ac53b356f0dd5ecc897bf0409f736a61f` |

This closes the prerequisite for implementing restored accounting. It does not
validate the implementation, every crash boundary, abort recovery, or full E6.
The exact hosted A0 case must run again after the cold construction path changes.

## Required invariants

- Complete durable evidence and the current retained V1/V2 physical proofs
  precede all restored charges. Install the full batch before sealing bootstrap
  and before the first risky allocation.
- Derive amounts from validated component policy. Durable receipts alone do not
  mint execution authority, capacity tokens, or cleanup permission.
- Require the Current deployment's exact running binding and its completed
  authorizing lifecycle graph. The authorizing operation can pin the prior
  Current or an earlier authenticated ancestor. Match the exact authorizer job
  and revision; do not assume that its pinned deployment is the immediate
  predecessor. Older completed operations remain descriptive history.
- Admission uses `Q <= max(0, F - D - L)`: physical free capacity, minus restored
  configured debt and live reservations. Current workloads do not obtain a
  second free share.
- Pending-publication tokens refuse consumer effects. Registry publication makes
  the same exact tokens usable atomically. Ambiguous publication retains charges.
- Both allocation paths hold the same coordinator borrow through installation
  or release. Abort fences new work, drains admitted operations, and releases
  only a definitely unpublished graph outside internal locks.
- Published charges release once under exact later predecessor authority, after
  gateway unlock and fresh proof of physical absence. Aliases and ordinary
  `Leave` cannot release them.
- Running but unhealthy Active workloads remain charged. Existing Healthy-state
  requirements remain unchanged.

## Regression baseline

Before production edits, the original regression file was copied unchanged onto
the accepted PR #247 base. Fresh pinned compilation and discovery of both parents
passed. `TestGeneratedDomainColdOwnershipWithoutChargeRefusesRetirementRoster`
passed. The restart regression
`TestGeneratedDomainRestartAccountingCompletedCurrentSupportsDistinctReplacement`
failed at the expected `Complete` boundary with
`generated candidate receipt does not match` (1.26 seconds for the outer test).
This records the original fixture's failure, not accepted regression coverage
or passing implementation evidence.
The unchanged regression source SHA-256 is
`7c07151178e7e8cf4a245d7cc8f36af5a185ac58a7ef7d592304151f2fa11765`.

Integration review then identified a limitation in that fixture: its first
deployment used a revision-zero `legacy_uninitialized` binding. Its real
042/044/045 records did not supply the complete authorizing lifecycle graph
required by the approved restoration contract. The resulting cold-ownership
refusal is correct. A proposed compatibility relaxation was rejected before
execution. The fixture was corrected through existing lifecycle producers,
and the corrected fixture was also tested against unchanged production code.

The corrected fixture establishes a completed lifecycle operation for the prior
Current and creates its successor through the existing running-binding producer.
On unchanged PR #247, this fixture refuses earlier, during cold group
reconstruction: the historical authorizing operation sets `lifecycleRetained`,
which that path rejects before reaching `Complete`. Independent review confirmed
this as an earlier cold-reconstruction gap. The correction checks completed,
cycle-free predecessor ancestry and the Current binding's exact authorizer;
it preserves normal candidate-admission refusal and physical-absence checks.
The corrected baseline target failed at that earlier boundary in 1.24 seconds,
with compilation, exact parent discovery, and the cheap refusal control passing.
Its source SHA-256 is
`50263c508b577e73bc97f92c8e608cb5e6e148283441cf355073f550e6b42072`.

Later fixture corrections preserve that canonical authority graph and change
physical dispatch, health modeling, and assertions after the earlier refusal.
They do not establish a second baseline pass or replace the recorded failure.

## Local implementation evidence

Fresh binaries built with Go 1.26.7 on Windows passed 59 distinct focused test
names (19 parents and 40 children), with no failures or skips in the accepted
streams. The evidence combines source-bound runs rather than counting repeated
isolated-process output as additional tests:

| Accepted run | Distinct names | Coverage |
| --- | ---: | --- |
| v13 ingress controls | 52 | Debt arithmetic, bootstrap, publication and abort fencing, both allocation paths, exact restored and ordinary tokens, complete evidence and legacy absence, ancestry, memory/disk release and refusal of a second share |
| v16 typed-store controls | 6 | Independent startup projections, canonical deployment bindings, lifecycle history, and mismatch refusal |
| v21 restart regression | 1 | Authentic completed history, unhealthy Active Current, fresh cold construction, exact restored token and configured charge, distinct replacement, and predecessor completion |

The final regression passed at all three process-isolation levels in 1.93
seconds. It directly checks that the restored Current has a published configured
token with no ordinary lease, and that the registry, budget, authority and ledger
share the same coordinator. It also checks exact receipt, owner, epoch, memory
and disk values and zero outstanding borrows. The unhealthy control changes
only the health observation of an otherwise identical running Active workload.

The regression does not count ordinary allocation calls or independently prove
release-once arithmetic. Those properties are covered by the focused controls
and the source-bound token checks. Setup capacity alone is not evidence of the
restart accounting rule.

Production bytes in the final v21 freeze match the reviewed v13 production
bytes. The accepted typed fixtures match v16. Read-only formatting checks,
`go vet ./internal/generatedingress ./internal/deployments
./internal/generatedruntimestate`, `scripts/check-generation.ps1`, and
`git diff --check` passed. All 19 source-file hashes remained stable after those
checks. Earlier failed or incorrectly selected development runs are retained as
failures and are excluded from the accepted union.

| Evidence | SHA-256 |
| --- | --- |
| Final v21 source manifest | `c83ca7d45e09d373acddeffc6b15d156bb697d5f3f39761cbf2620935f22cf4b` |
| Final restart regression source | `944e28c4aabdc821c9e4d1420be395904b7124cd6d1ec7b6eb0eb92a03aabbd2` |
| Final ingress test binary | `a651e9a9034acd2f93e93a54d1956c2e937328df56a32f292e6ead2d9939b7c1` |
| Restart result receipt | `6df2533520fb3073cff54b9d061a8df5ea5fbc8438466eea375214d1fdc59b0e` |
| Restart raw output | `4c1285d8ff74cac39ded811faa4e536cbab2b4dc1bf9645ed05b9f3c5a9ae1d9` |
| Static checks and source stability | `2dbdf1dc31983e31f016807b6f5edd47b2dfe76138306ae068c7b0726636c4e1` |
| Independent source/security closure | `201aa5adbd63ba119a536d36a569527b9878d28ce541544c886778017c979c81` |

## Remaining acceptance and rollback

The local evidence does not establish Docker behavior, Linux race safety, every
interrupted durable state, or complete E6 recovery. Hosted checks must exercise
the changed cold-construction path, including the previously accepted A0 case.
Existing failures in separate retained-V2 fixture deadlines and other hosted
recovery cases remain open; they are not reported as passing here.

This change introduces no schema migration or persisted capacity authority. It
derives in-process charges from existing authenticated records and validated
policy. There is no new public API or feature flag. Rollback is a normal revert
of the cohesive change, preserving durable records and immutable history; it
also restores the previous cold-reconstruction limitation. No live controller
was refreshed and no deployment or target-branch merge was performed for this
verification.
