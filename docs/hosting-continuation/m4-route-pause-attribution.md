# M4 generated route-pause attribution

Recorded 2026-10-22 against [PR #206](https://github.com/tyhuang9/rig/pull/206)
at `5e6c130e9b42dfa736c448ea905f76ed1291b829`.

## Purpose and behavior

Hosted controller, migration, LAN and restart journeys still pause at
`route_reconciliation_required`. The existing public diagnostic does not
identify which internal boundary paused. This change adds an optional structured
warning with a fixed message and a closed stage/cause pair at those boundaries.

| Internal stage | Observation |
| --- | --- |
| `runtime_begin` | Another generated deployment is in progress |
| `runtime_success_head` | Reading the committed active head failed, or the head differs |
| `route_switch` | Route switching or its state persistence failed |
| `previous_retirement` | Previous-slot retirement refused or failed |
| `runtime_success_advance` | Final runtime-state advancement failed |

Causes are classified through existing typed errors and helpers. Unknown causes
remain `unknown`; invalid stage/cause combinations are clamped or omitted.
The observation adds no raw error, identifier, path, command, environment,
configuration or secret. The normal daemon uses its existing structured logger;
a nil logger disables the observation. Logger write failure does not alter the
existing pause result.

All five pause decisions retain their original waiting result and nil error.
Retirement authority, active-slot preservation, durable state, cleanup and effect
ordering remain unchanged. This is diagnostic work, not a route/retirement fix.
There is no public response/schema change, readiness proof or new capability.

## Hosted fixture forwarding

Direct controller fixtures attach a dedicated route logger. Full-hostd fixture
classification keeps the existing stderr digest and can append the latest valid
closed route pair from a suffix of at most 64 KiB and 64 records. It requires
exact unique case-sensitive keys and string values; malformed, duplicate,
case-folded, invalid and unknown records do not manufacture an accepted pair.
Ancillary fields are discarded. Startup classification remains independent.

Process-kill fixtures previously discarded restarted-child stderr. They now
capture it privately before starting the child. Classification is read only after
`Wait` has completed and closed the completion channel. Failed-test cleanup
kills/reaps an unfinished child before emitting the digest and closed result;
expected stops are suppressed. Raw stderr is never forwarded to test output.
The full private capture remains in memory: the limits bound classification,
not the total capture allocation. A diagnostic is observational and confers no
resource ownership or permission to retire anything.

## Local acceptance

Pinned Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`, amd64.
Windows binaries and native Linux binaries through WSL ran from their package
directories with the exact expected parent census, `-test.count=1` and
`-test.timeout=5m` selectors. Every expected name ran and passed; no standalone
`-test.list` discovery artifact was retained.

| Check | Result |
| --- | --- |
| Generated executor, each platform | 7 parents and 26 children passed |
| Tagged hostd closed-diagnostic controls, each platform | 1 parent and 59 children passed |
| Complete selected RUN/PASS census | Exact across both platforms; zero failures/skips/duplicates/missing passes |
| Scoped executor/hostd vet and live-tag hostd vet, both targets | Passed |
| Windows/Linux hostd production builds and selected test binary compilation | Passed |
| Formatting, canonical `pwsh -NoProfile -File scripts/check-generation.ps1`, diff check | Passed |
| Executed-output secret/path negative checks | Passed |

The first tagged Windows hostd compile found a missing standard-library `bytes`
import in `hosting_process_kill_live_test.go`. That output is retained. Adding
only the import and formatting corrected compilation; the source was frozen
again and all accepted checks passed. Source-quality, security and observability
reviews are separate from executable evidence.

The selected executor parents are:

```text
TestGeneratedExecutorRoutePauseAttribution
TestGeneratedExecutorRoutePauseCauseClassification
TestGeneratedExecutorRoutePauseDiagnosticControls
TestGeneratedExecutorFinalizesCommittedRuntimeAfterRestart
TestGeneratedExecutorNeverCleansNewSlotAfterActiveCAS
TestGeneratedExecutorRetriesOldSlotCleanupWithoutTerminalizingDeployment
TestGeneratedExecutorFinalAdvanceFailureRetainsRecoverableRuntimeState
```

The hostd parent is `TestLiveHostdRebindClosedDiagnosticControls`, compiled with
`live_docker` but executed as deterministic local controls without Docker.
Coverage includes every pause, the committed-head read-error branch, success
without a diagnostic, logger failures, nil logging, exact cause classification,
parser ambiguity, suffix limits and capture-before-reap refusal. The capture
control simulates completion; hosted tests must verify actual subprocess reaping
and forwarding.

## Evidence and remaining acceptance

Local evidence is retained under
`temp/m3-lan-external-db-20261015/m4-ci-baseline-verification/route-pause-attribution-20261022/`.
The executable receipt records 18:55:15Z (the first failed hostd compile) to
19:00:10Z; the earlier formatting log began at 18:54:14Z. The receipt's start
field therefore does not cover that formatting step. Its
`local-verification-20261022T191500Z` directory name is a label, not an execution
timestamp.

| Artifact | SHA256 |
| --- | --- |
| Executable receipt, `089-verification-receipt.json` | `faad0f51d002c42a4cd80d21ad4b480cadbd9f861ab79e5b689a6bce3c04b706` |
| Evidence manifest | `324bc84d80fe2052c20f5f2af7d00db2a8e771bc6764013e640592f09019376a` |
| Final 13-file source manifest | `cd665e3cdb9a416213324215c344f9a8d9519385878c1002d01c7396111d51fb` |
| Post-verification 1,050-file Go/module manifest | `e3d5393dda0b5b8ab3db17607c825e199b1cedf7ad75d6293311073d1b254702` |
| Source-quality review | `267f5a20453e48d92afd357f597498fa69042b9825d7b7404e8f2b376dbccf22` |
| Initial security review | `e0d3a380f56be903d8e1eb68414102e4fe182245364c0879785d9e148bff4652` |
| Additive capture security review | `d8f195fca3aeb7107b9c712d08039a6b68a56bda0d0a428e8442174460da8322` |
| Observability recheck | `c17cb5ccb9ec40c2ed0c3a5526a1304ae1a185f2077674b7a7ef128cbe0feb5c` |

The full Go/module manifest was recorded after tests for publication/documentation
checks, not as a pre-test freeze. Formatting and the missing standard import are
the only changes after specialized source review. The initial observability hold
for discarded child stderr remains recorded alongside its corrected recheck.
Final evidence QA and documentation check receipts are recorded in the draft PR.

No local Docker, race, full hostd suite or broad repository suite was run.
CodeRabbit CLI was unavailable. Hosted CI must show these diagnostics on the
actual six affected journeys, which may still fail at the unchanged guard. The
cause will determine the subsequent runtime fix; this change does not complete
M4 or permit bypassing retirement validation.

Rollback is a normal revert of the diagnostics and fixture forwarding. No
persisted state, migration, deployment or live controller activation is involved.
