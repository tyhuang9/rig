# M3 typed runtime race partitions

Status: local selector repair verified on 2026-10-16. This records CI
selection and capacity-planning evidence. It does not establish a fresh hosted
race result or Docker acceptance.

Base: `4289f02c2001379703559665b7d525b3fa05f733` (PR 174), branch
`fix/hosting-m3-typed-runtime-partitions`.

## Frozen timeout evidence

The retained [GitHub Actions job 116795247381](https://github.com/tyhuang9/rig/actions/runs/38845583159/job/116795247381)
ran commit `4289f02c2001379703559665b7d525b3fa05f733`. Its checked-out tree
matched the commit tree. The raw log is retained outside the checkout at
`temp/m3-lan-external-db-20261015/pr174-live-wait-ci-evidence/116795247381/job.log`:
216619 bytes, SHA-256
`b83d3023f624815d139878fb579328c8579719517cbcefd0106041f8c42346e4`.

Thirty-eight typed-runtime parents started. Thirty-seven passed, no parent
failed, and their elapsed sum was 1896.98 seconds. The package failed at its
unchanged 1920.048-second timeout. The active parent had run for 23 seconds;
its `late_bound_volume_drift` child had run for eight seconds after a 15.33
second sibling pass. The static selected set contains 39 parents, leaving one
StageRuntime parent unstarted.

The separate [job 116795247639](https://github.com/tyhuang9/rig/actions/runs/38845583198/job/116795247639)
reproduced the same package timeout from the same head and checkout tree. Its
232280-byte raw log is retained at
`temp/m3-lan-external-db-20261015/pr174-live-wait-ci-evidence/116795247639/job.log`,
SHA-256 `647a9da253436d9cae01c9ab27de0be4be7a70392e9ad46e15daa03327cdc4a1`.
It started 38 parents, passed 37, and had no parent failure before the
1920.04-second package timeout; completed parents totaled 1857.94 seconds. Its
active last parent had run for 62 seconds and its active
`late_authority_drift_before_start` child for one second.

The first job's timeout snapshot was still constructing the active child fixture:
`newGatewayRebindTypedStageRuntimeFixture` → `newGatewayCurrentStateFixture`
→ final-config setup → `installExact` → protected-history scan → bounded
`readStrict` JSON decode. The fixture had not reached the drift callback or
`serveStage` assertion. This supports an aggregate-capacity repair; it does not
prove an individual test hang or a new functional defect.

## Partition

`rebind-typed-handover` selects the complete
`^TestGatewayRebindTypedHandover` parent family. The existing
`rebind-typed-runtime` fallback retains the complete typed family while
excluding the withdrawal, completed, and handover families. Each selected
parent therefore retains its complete subtest tree. New handover names enter
the handover group; existing withdrawal and completed families retain their
dedicated groups, and other future typed-runtime names enter the fallback.

The retained completed timings place 15 handover parents at 1186.29 seconds
and the complement's 22 completed parents at 710.69 seconds. The complement
also owns the two unfinished StageRuntime parents. These timings guide the
unchanged 32-minute per-package deadline; they are not a performance guarantee.

All three workflow matrices contain the same added handover row. Race mode,
`-count=1`, the 32-minute package timeout, the 34-minute CI step timeout, the
42-minute job timeout, failure gates, and `fail-fast: false` remain unchanged.

## Verification recorded

- Before the change, the retained-jq shell contract passed its 15 composition
  invocations and the repository canonical generation check passed.
- After the change, `bash -n scripts/check-generated-runtime-race.sh` and
  `bash -n scripts/check-generated-runtime-race_test.sh` passed.
- With jq 1.8.1 retained under `temp/m3-repository-race-partitions-20261010`,
  `bash scripts/check-generated-runtime-race_test.sh` passed: 27 exact
  default/race/plain invocations, all three workflow matrices, and 14
  refusal/failure cases.
- The post-change canonical command
  `pwsh -NoProfile -File scripts/check-generation.ps1` passed with Go 1.26.7,
  `GOTOOLCHAIN=local`, and readonly module flags. Its controller contract took
  0.662 seconds; the retained output is
  `temp/m3-lan-external-db-20261015/typed-runtime-partitions-verification/generation.log`.

Independent static QA scanned 1075 top-level generated-ingress parents. Its Go
RE2 audit found 26 nonempty partitions with every parent owned exactly once.
The old typed-runtime set of 39 parents is now 15 handover plus 24 fallback
parents; withdrawal remains five parents and completed remains nine. QA found
no membership changes outside the handover transfer.

The QA mutation contract rejected all six missing/duplicate handover-matrix
rows across the three workflows. It also rejected both selector-ownership
mutants: narrowing the handover family while retaining the stale exclusion,
and omitting the fallback handover exclusion. Both mutants failed the exact
shell contract and the Go RE2 ownership audit. The retained QA results are
`temp/m3-lan-external-db-20261015/typed-runtime-partitions-verification/qa/go-re2-audit-summary.json`
and `mutation-contract-and-source-ownership-results.json`.

No local race-enabled package, Docker job, or hosted workflow was run for this
selection-only change. A fresh hosted matrix remains the acceptance evidence
for capacity and race behavior.

Rollback reverts this CI-only commit series. It changes no application behavior
or data.
