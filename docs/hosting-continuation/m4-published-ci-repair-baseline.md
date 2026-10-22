# M4 combined CI repair baseline

Recorded 2026-10-21. This topic combines already published repairs with the
generated-domain physical provenance foundation so hosted CI can test their
interaction. Its base is draft [PR #200](https://github.com/tyhuang9/rig/pull/200),
`63e38af590d75e318a544da82fdfcf4abeb6b0c2`.

## Immutable inputs

| Input | Head | Purpose |
| --- | --- | --- |
| [PR #202](https://github.com/tyhuang9/rig/pull/202) | `e5ac3de6c5642443b8e0579889ebeeb7334be252` | Typed Docker CPU inspect fields for migration inspection |
| [PR #203](https://github.com/tyhuang9/rig/pull/203), including #201 | `44570972f612e06330c448ae4703def63f890e90` | Real fixture configuration and execution authority; original published rebind history |
| [PR #204](https://github.com/tyhuang9/rig/pull/204) | `bc7e1dc771c4587be02838eb95373f8227e9d1af` | Eight existing schema-fixture substitutions, including the generated-image follow-up, with measured local test-cost evidence |

The local integration preserves each input's commits and evidence. It introduces
no schema migration, generated-history rewrite, production activation, managed
database, or database provisioning.

## Conflict resolution

The foundation moved candidate ownership into ingress, so ingress package tests
use an external test adapter to avoid importing the runtime package in a cycle.
The incoming fixture repair predated that adapter. The resolution preserves the
adapter and the incoming original database, authority, engine and candidate
session. Candidate conversion retains every current scalar field; published
history validation receives the original context and database unchanged.

The existing network cancellation fixture also needs its test-only adapter
method. Review found the missing method before execution; the interface and
external adapter now forward the original test, context and deployment ID to the
real authority. Assertions and cleanup remain intact.

Combined compilation also found an incoming negative test that still named the
candidate execution type in its former deployments package. Commit `63752f8`
updates that test's import and zero-value fixture to the existing ingress owner
type, matching the session field. Production code and assertions are unchanged.
The original compile failure is retained alongside the subsequent verification.

The synthetic rebind runtime seed and duplicate application insertion remain
removed. No validator, ownership guard, retirement guard or test assertion is
weakened to make the merged fixture pass.

## Acceptance checkpoint

The input branches retain their individual evidence and limitations. The image
follow-up's full Linux package failed at two existing Node prerequisites in WSL,
and its Docker and platform-specific skips remain unverified. Its focused fixture
comparison and static checks passed; those results are distinct from full-package
acceptance. See the retained fixture-cost document for exact counts and receipts.

Combined verification passed at source commit
`63752f8878933035c5286aeb64627e3b3a2d6d8a`, tree
`a7eb326fcdba8a7386f013cd9fd83c24f0c34996`. Each platform has 36 selected
parents and 110 subtests, all passing with zero skips. Windows retains the
earlier 12-parent deployments and five-parent executor results on unchanged
package inputs. The remaining six Windows groups and all eight Linux groups ran
after the test-type correction. Linux binaries executed natively through WSL;
cross-compilation alone is not counted as execution.

| Package | Parents per platform | Subtests per platform |
| --- | ---: | ---: |
| deployments | 12 | 84 |
| generatedexecutor | 5 | 6 |
| generatedruntime/liveauthority | 8 | 0 |
| generatedimage | 3 | 14 |
| generatedingress | 3 | 0 |
| cmd/hostd, local composition fixture with `live_docker` tag | 1 | 2 |
| applifecycle | 2 | 0 |
| composeruntime | 2 | 4 |

Verification used pinned Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`, and explicit
Windows/Linux amd64 targets. Each selected binary was discovered before execution,
then ran once from its package directory with a ten-minute timeout. The exact
parent selection is listed below. RUN/PASS parity, source and binary hashes were
checked; no selector or deadline was relaxed.

All 59 commands in the final continuation passed. These include the remaining
test groups, scoped vet and hostd builds for both targets, compilation of the
hostd/ingress/image live Docker tests, discovery of six live fixture parents per
platform, canonical `pwsh -NoProfile -File scripts/check-generation.ps1`, and
the diff check. Formatting covers all 23 aggregate changed Go files: 22 unchanged
earlier comparisons plus the corrected test file. Live fixture discovery and
compilation do not establish Docker execution.

Three failed attempts remain recorded: two verification-script census omissions
(three release-strategy cases, then four candidate-capacity cases) and the stale
test-type compile error. The first two attempts' raw tests passed; their harness
receipts remain failed. Reconciliation reused those logs without rerunning their
unchanged packages. The corrected global source inventory is different from the
earlier inventory by exactly the liveauthority test file; earlier results are
not attributed retroactively to the newer global inventory.

Local raw logs, binaries, command arguments, rosters and source inventories are
retained under `temp/m3-lan-external-db-20261015/m4-ci-baseline-verification/`.

| Evidence | SHA256 |
| --- | --- |
| Final executable receipt, `combined36-continuation3-utc-20261021-072402/receipt.json` | `8ff352f27a74d0a74993e11b875469c4382ff4d9515fdd4e1a05d72056d843f5` |
| Final 1,048-file Go/module inventory | `e5208b03778fc6def69d64ca94e5a53167ee9831ae7df00b30465c3c3f470aed` |
| Earlier inventory for retained Windows package results | `11c970f35b9aa3d9ddc345a206a92ec97f9b99e7afd948dd1eedb8ceca757975` |
| First census-failure receipt | `39349e211971a19d5ec365503f279138ba3ac21c608d9943848f566288a35e6d` |
| Second census-failure receipt | `05ca454a15612ee3d2b351eff8052f46a719f7ebd9fa043dd270140388480814` |
| Compile-failure receipt | `1efa97a4b6ee616059d5de29a20d9a14b2a946cfd1fe344a199f0931bee4a966` |
| Test-type alignment source review | `b2a272c7d0ae1ebc6f225255f81b0339d71facb2a6bda08d30a511d30b40c06f` |

Documentation workflow, production documentation build and accessibility checks
are run against this document before publication. Their receipt and final review
are retained beside the executable evidence and recorded in the draft PR.

Hosted Docker and Linux race checks are required after publication. Known older
heads fail during fixture setup, migration inspection, and TLS replacement
reconciliation. The TLS retirement/route boundary is still unfinished; this
baseline does not claim to complete M4. A child process exiting before readiness
also needs the repaired fixture exercised on the hosted runner.

## Exact selected parents

For each package below, join its names with `|` inside an anchored
`^(...)$` selector. The equivalent local Go command is
`go test -count=1 -timeout=10m -run '<selector>' <package>`, with the pinned
environment above. Use `-tags live_docker` only for the listed hostd composition
parent. These are selected integration checks, not complete package suites.

### `./internal/deployments`

```text
TestGeneratedMigrationInspectionProjectionIsValidAndSecretFree
TestGeneratedMigrationInspectionRequiresCompleteUniqueTypedProjection
TestGeneratedMigrationInspectionPreservesUncertainAbsenceCause
TestGeneratedMigrationRequiresNameAbsenceBeforeCreate
TestGeneratedMigrationRequiresCanonicalImageAndNetworkBeforeCreate
TestGeneratedMigrationRevalidatesOwnedContainerBeforeStartAndRemove
TestGeneratedMigrationPrivateObserverDerivesProtectedInputsAndPersistsSuccess
TestGeneratedMigrationLostMutationAcknowledgementAllowsCleanupOnly
TestGeneratedMigrationPostObservationGatesRefuseDrift
TestGeneratedMigrationRemovePreservesUncertainMutationCause
TestGeneratedMigrationCanceledPositiveContextRetainsBoundedCleanup
TestReleaseRuntimeStrategyComesFromPinnedPlan
```

### `./internal/generatedexecutor`

```text
TestGeneratedExecutorRejectsCallerOnlyMigrationOutcomes
TestGeneratedExecutorPersistsOnlyPrivatelyObservedNonzeroMigrationExit
TestGeneratedExecutorRetainsCandidateRiskOnSucceededHandoffUncertainty
TestGeneratedExecutorRetainsCandidateCapacityOnImageRefusalWithPriorCandidateRisk
TestCapacityPausedDeploymentResumesWithRecordedPinsAndOneReplacement
```

### `./internal/generatedruntime/liveauthority`

```text
TestAuthorityConstructorDoesNotAcquireEffectsScope
TestPrepareCandidateUsesProtectedStoresAndClearsEnvironment
TestBindNetworkCreatesOnlyGenuineBuildingHistory
TestPrepareNextCandidateRecordsFixtureNodeVersion
TestBindCandidateReplayPreservesOriginalClaimAcrossSequentialScopes
TestPrepareCandidateClearsStaleDeploymentCapabilities
TestBindCandidateAllowsSelectedConfigurationAfterReleaseOriginal
TestPublishedHistoryRefusesIncompleteMismatchedAndForeignStateWithoutWrites
```

### `./internal/generatedimage`

```text
TestBuilderManagerFailsClosedOnAmbiguousMissingInspections
TestCompilerStagesStandaloneRecipeFixtures
TestCompilerPreservesCommittedReadyResultWhenCommitAcknowledgementIsLost
```

### `./internal/generatedingress`

```text
TestLiveGeneratedAuthorityAllowsSequentialEffectsScopes
TestLiveGeneratedAuthorityUsesCallerDatabaseAcrossSequentialBindings
TestLowIngressCapacityBecomesReplacementCapacityDiagnostic
```

### `./cmd/hostd` with `live_docker`

```text
TestLiveRuntimeCompositionAcceptsFixtureRunnerPointers
```

### `./internal/applifecycle`

```text
TestApplicationDesiredLifecycleTransitionsAndHistoricalReplay
TestPositiveFenceValidatorUsesCallerTransactionForOperationAndKeyPage
```

### `./internal/composeruntime`

```text
TestExecutorManagedWorkspaceBindIsReadOnlyForGitHubAndLocal
TestPolicyCanonicalScopeFingerprintAndRepositoryParity
```

## Hosted follow-up: original stage-fixture database

On 2026-10-22, PR #206 at
`a3bc51e04c3d7b79b428f2c12923c72b3646a70d` had 117 successful checks,
17 failed checks and one skipped check. The 15 failed source jobs checked out
merge `70f4b77db3edc5a9498ea4171330ac31f374bb2b`, whose tree
`41a4fa1d1903e64c871cd3b5e7c1394238568a8a` equals the published head tree.
The other two failures were aggregate jobs.

Eight Docker jobs, covering eleven live parents, refused setup because six
stage helpers reopened the same database path and constructed a second
repository. The authority deliberately requires the original database pointer.
Each helper now uses `fixture.db` and `fixture.repository`; its redundant open,
close registration and database import are removed. The shared fixture retains
database cleanup ownership. Every assertion, lost-acknowledgment/proof-failure
case, resource cleanup and timeout remains unchanged. Independent child-process
database opens remain intact. The production guard is unchanged.

The affected helpers are network, config volume, data volume, stopped container,
config copy and start under `internal/generatedingress/gateway_rebind_stage_*_live_test.go`.
Final-config-copy tests use the repaired start helper indirectly. The focused
source review found exactly six insertions and 42 deletions across those six
test files, with no production source or workflow change.

### Follow-up verification

Pinned Go 1.26.7 and the same readonly, non-race environment described above were
used. Windows and Linux live-tag ingress vet and test-binary compilation passed.
These three selected parents passed on Windows and native WSL Linux, with zero
subtests, failures or skips on either platform:

```text
internal/generatedingress (live_docker):
  TestLiveGeneratedAuthorityAllowsSequentialEffectsScopes
  TestLiveGeneratedAuthorityUsesCallerDatabaseAcrossSequentialBindings
internal/generatedruntime/liveauthority:
  TestPublishedHistoryRefusesIncompleteMismatchedAndForeignStateWithoutWrites
```

Canonical generation and diff checks passed. Six-file source hashes were stable
through verification. The original receipt is a prose command summary with raw
logs and binary hashes, rather than a timestamped per-command ledger; silent
vet/compile output is supported by the recorded command exit observations.
An initial PowerShell flag-tokenization failure occurred before test execution
and remains in its original log. Corrected discovery and execution passed.

Root subsequently discovered all eleven affected hosted parents in each existing
binary, without running Docker tests. They share the prefix
`TestLiveGatewayRebindSuccessor` and these suffixes:

```text
NetworkStage
ConfigVolumeStage
DataVolumeStage
StoppedStageContainer
StageConfigCopy
StageConfigCopyLostAcknowledgmentAdopts
StageStart
StageStartLostAcknowledgmentAdopts
StageStartProofFailureWithdraws
FinalConfigCopy
FinalConfigCopyLostAcknowledgmentAdopts
```

The complete 1,048-file Go/module manifest was recorded after the tests and is
not claimed as a pre-test full-source freeze. It records the six-file source
transition from the earlier baseline. Documentation checks bind to this later
manifest. The local evidence directory is
`m4-ci-baseline-verification/stage-fixture-repair-20261022/` beneath the existing
temporary evidence root.

| Evidence | SHA256 |
| --- | --- |
| Worker verification receipt, `20261022T130357/verification-receipt.txt` | `973c90beb27c4ec482822f9a40df1aa15c5687a599e8222b44ff0c811f0b7e05` |
| Worker evidence inventory, `20261022T130357/final-evidence-sha256.tsv` | `32535a2eb6b942bfeaf17e07bda6ac02e2c52c9df6ebf332553cd05cb482099d` |
| Independent six-file source review | `93d32d460bb28d831714e9eac3b6bf3f5956b1b821ee5e097e87aac358b55ad5` |
| Additive hosted-parent discovery and prior JSONL audit | `c93c2fd5ef3500a405b08fd8bebe88379ea170c8168b1599465f8724d202062b` |
| Post-verification 1,048-file Go/module manifest | `d1f69833b7310684dc71be8b3343ff87bf2fa127260417e1cb1ddb1c1922677b` |
| Exact-head hosted failure triage | `fcc67239903ac3bd34d2f4497f2d34e4446965bb4f650b124f5fa52f1598c899` |

This repair needs hosted Docker execution. Six separate route/restart jobs still
pause during reconciliation; their precise failing branch remains under
investigation. The ingress race remainder exceeded its unchanged 32-minute
budget after 252 parent passes, 24 skips and one interrupted parent. Its sampled
migration stack does not establish a deadlock. The two aggregate failures follow
those failed jobs and do not independently establish a PostgreSQL regression.
These failures remain open; local fixture checks do not complete M4 acceptance.

## Rollback and limits

Revert this topic's integration if its combined behavior is unsuitable. No data
rollback is needed because these changes introduce no persisted schema or
history rewrite. The component repair documents retain their detailed scope,
verification and limits. The private registry/fence and startup snapshot work
are separate units and are not included here.
