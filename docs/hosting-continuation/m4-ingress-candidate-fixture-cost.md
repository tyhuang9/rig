# M4 ordinary candidate fixture cost

Recorded 2026-10-22 against [PR #206](https://github.com/tyhuang9/rig/pull/206)
at `5e6c130e9b42dfa736c448ea905f76ed1291b829`.

## Change and reason

The ordinary generated candidate test fixture now uses the existing
`internal/testsupport/databasefixture.Open` helper. Each test still receives a
private `t.TempDir()/state` database. The helper copies only the production
schema and static migration data, then uses the production database opener for
connection settings and migration-ledger validation. Per-test rows and cleanup
remain private.

The source patch changes one import and one call in
`internal/generatedingress/generated_candidate_fixture_test.go`. The custom
commit-acknowledgment fault connector, sealed generated-domain identity fixtures,
production code, assertions, workflow selection and timeouts are unchanged.

PR #206's previous ingress race job reached its 32-minute limit. Its last stack
included schema migration, but that stack does not prove a deadlock or identify
all package costs. This patch removes repeated migration setup from the ordinary
candidate fixtures; hosted CI must determine whether the complete race gate now
fits its existing limit.

## Executable evidence

Pinned Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`, amd64.
Linux binaries ran natively through WSL from the package directory. The selected
binary runs used `-test.v=test2json`, an anchored parent selector,
`-test.count=1`, and `-test.timeout=10m`.

| Check | Result |
| --- | --- |
| Ordinary affected tests, Windows | 40 parents and 62 children passed; zero skips/failures |
| Ordinary affected tests, native Linux | 40 parents and 62 children passed; zero skips/failures |
| Normal and `live_docker` ingress test compilation, both targets | Passed |
| `go vet -tags live_docker ./internal/generatedingress`, both targets | Passed |
| `go build ./cmd/hostd`, Windows | Passed |
| Windows `go list -deps ./cmd/hostd` | Fixture helper absent from production dependency graph |
| Changed-file `gofmt -d`, canonical `pwsh -NoProfile -File scripts/check-generation.ps1`, diff check | Passed |

The initial census found 35 parents and 59 children. Independent review found
five more parents through the failure-owner fixture wrapper. Those five and
their three children passed separately on each platform using the same source
and binaries. The retained union is 40 parents and 62 children; the original
35 were not rerun. The original census and dated correction remain in evidence.

A fixed native-Linux comparison ran these three parents three times each:

- `TestGeneratedCandidateFactorySharesPreAndPostCreationRepositoryCopies`
- `TestGeneratedCandidateOwnerCreatesStartsAndHandsOffExactCandidate`
- `TestGeneratedCandidateBeginCreatePreservesCommittedIntentWhenAcknowledgementLost`

Both baseline and candidate had nine parent and three child RUN/PASS events,
with no skips or failures. Package elapsed time changed from **7.456s to 2.537s**.
The custom commit-fault control retained its production migration path and similar
individual timings (baseline 0.52/0.53/0.55s; candidate 0.53/0.49/0.50s).
This local comparison is not a hosted race performance claim.

## Evidence identity and limits

Raw commands, environments, immediate exits, source/binary hashes, discovery and
JSONL are retained locally under
`temp/m3-lan-external-db-20261015/m4-ci-baseline-verification/candidate-fixture-cost-20261022/`.

| Artifact | SHA256 |
| --- | --- |
| Changed fixture source | `5202eab7787e84ad4bc788798bece38c74385bd6505bd8baea1d89ccb7d87487` |
| Verification summary | `83b3dbff5ecfc44cb55408b171080f2a17858c12c81a18dc471e570448a62242` |
| Final source/binary/log manifest, `021-final-source-and-evidence-manifest.log` | `6482c7aa35147f7867c34a7e9ddce7752c6f55d6dcde6bd1cba7e033f1551ed0` |
| Post-verification 1,048-file Go/module manifest | `60da5d19ac0498cd2138f563592021017455950e12f72be628cd832908c2fac9` |
| Independent source review | `bd083e324f1ee1c13c9fead4b21f1a4d2072c6266c0599f6ab1e6e2f0530421a` |

The full Go/module manifest was captured after the executable checks for the
publication and documentation checks; it is not represented as a pre-test freeze.
The changed-source hashes and executable evidence were recorded during the runs.
Documentation workflow, production build and accessibility check receipts, plus
evidence QA, are recorded in the draft PR.

Docker, Linux race, a full ingress package execution and a Linux production
hostd build were not run for this patch. CodeRabbit CLI was unavailable; source
review was performed independently. Existing route-reconciliation failures remain
separate work. This patch does not establish M4 completion.

Rollback is a normal revert of the one test fixture substitution, restoring its
production opener. There is no production behavior or data migration to disable.

## Complete ordinary parent selection

Join the following names with `|` inside `^(...)$`. Discover the exact parents
before execution; require matching RUN/PASS names and zero skips/failures.

```text
TestCandidateStoreCompositionPreservesSharedOwnerAndRejectsForeignStores
TestGeneratedCandidateFactorySharesPreAndPostCreationRepositoryCopies
TestGeneratedCandidateFactoryRejectsCompositionDriftWithoutMutation
TestGeneratedCandidateFactoryRejectsInvalidDependenciesAndDatabaseScope
TestGeneratedCandidateFactoryBuildsSanitizedFixedDockerEnvironment
TestGeneratedCandidateProcessOptionsRequireExactSafeBounds
TestGeneratedCandidateFactoryRejectsUnsafeCommandDirectories
TestCandidateIntentPreventsGenericTerminalWritesAfterCallerRead
TestGeneratedCandidateObservedCreateFailureRequiresConfirmedAbsence
TestGeneratedCandidateObservedHealthFailureRequiresConfirmedAbsence
TestGeneratedCandidateSuccessfulCandidateCleanupIsNotFailureEvidence
TestGeneratedCandidateRefusalsNeverMintObservedFailure
TestGeneratedCandidateFailureRefusesAfterHealthHandoffLatch
TestGeneratedCandidateFailureLateWriteErrorRollsBackEveryStateRow
TestGeneratedCandidateOwnerCreatesStartsAndHandsOffExactCandidate
TestGeneratedCandidateOwnerRejectsEmbeddedAndCrossScopeHandles
TestGeneratedCandidateOwnerCoalescesAliasObservationsWithoutDuplicateCommands
TestGeneratedCandidateOwnerRechecksAuthorityAfterEnvironmentStaging
TestGeneratedCandidateOwnerDoesNotHoldSQLiteWriterDuringProcessRun
TestGeneratedCandidateRejectedDiscoveryCannotRedirectCleanup
TestGeneratedCandidateUnquiescedCleanupBlocksHealthAcrossPins
TestGeneratedCandidateCreatePreparationCleansStagedEnvironment
TestGeneratedCandidateUnquiescedCreateRetainsStagedEnvironment
TestCandidatePolicyUsesOriginalReceiptAttemptAcrossResume
TestCandidatePolicyCreateArgumentsMatchHardenedRuntimeAndOptionalEnvironment
TestCandidatePolicyDigestBindsStableInputsAndExcludesProcessLocalPaths
TestCandidatePolicyRejectsBrokenReceiptAndArtifactLinks
TestCandidatePolicyRejectsInvalidRuntimeComponentBoundaries
TestCandidatePolicyNextJSAddsOnlyFixedCacheTmpfs
TestCandidateCommandPreparationRechecksDirectoriesAndCopiesInputs
TestCandidateValidationPinsCopyFindingsAndProtectedRoster
TestCandidateAdmissionAllowsReadySelectionAfterPendingBaseline
TestCandidateExecutionAcceptsReceiptBackedStartingAndPinsArtifact
TestCandidateExecutionPinsExactReadyArtifactAfterFirstExecutionValidation
TestCandidateAdmissionAllowsReleaseOriginalConfigurationToDifferFromSelectedConfiguration
TestCandidateValidationRejectsInconsistentMainAndMigrationStages
TestCandidateRawSnapshotSurfacesReceiptRiskBeforeStrictMismatch
TestCandidateAdmissionReturnsRiskBeforePolicyRefusal
TestCandidateValidationRejectsConfigurationSourceAndRosterDrift
TestCandidateExecutionRequiresExactPersistedPolicyAndStage
```
