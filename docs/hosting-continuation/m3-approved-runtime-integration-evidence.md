# M3 approved local runtime integration

Status: the three explicitly approved local integrations are complete and the
combined scoped checks passed. Actual Docker acceptance and M3 completion remain
pending. This approval did not authorize publication, a GitHub merge or deployment.

On 2026-10-07 the user approved integrating the three reviewed commits below into
`feature/hosting-m3-rebind-cross-store-runtime`, starting at
`b813eff283a2590cbdabd76fd1b9044ef36d0a6f`.

| Reviewed source | Purpose | Preserving local merge |
| --- | --- | --- |
| `6924a32d8efb0725e76ec5d00f59081328ebe121` | Startup ordering and same-Manager reuse | `07c7f59` |
| `7435e23488e4844f033a45277fcb98981c8e503c` | Retained legacy-source compatibility | `d849635` |
| `8826dd36e5be2788ab11b24a6049fd9f1b2f5a51` | Real Docker commit/rollback acceptance gate | `709b61f` |

The merges had no conflicts. Every source commit remains an ancestor of the
integrated head. All 21 integrated files (12 startup, 4 compatibility, 5 Docker
gate) have exactly the same Git blob IDs as their reviewed source commits. There
was no rebase, squash, conflict-resolution edit or production factory activation.

Tested head: `709b61f93289a5dd10e2a9eef7237fed03735ccb`.
Tested tree: `44010a4c7353a9ee02dad0da8a1f6241a20b5071`.
This evidence-only addition follows the tested source tree.

## Combined verification

The independent integration review selected this bounded gate because the source
changes have disjoint file ownership and their reviewed contents survived exactly.
The earlier long composed lifecycle results remain recorded in the individual
evidence documents; they were not rerun or represented as new combined results.

```powershell
$tests = @(
  'TestGatewayRebindStartupDispatchPreservesDedicatedRecovery',
  'TestGatewayRebindStartupActiveResultAndLeaseOrdering',
  'TestGatewayRebindStartupRefusesUnverifiedRecovery',
  'TestGatewayRebindStartupFreshDatabaseAndManagerReuse',
  'TestGatewayCurrentRecoveryModePreservesMarkers',
  'TestInspectGatewayRebindCurrentSeparatesSelectedAuthorityFromActivePhase',
  'TestGatewayRebindAttemptViewPreservesLegacySourceWithoutChangingFormat',
  'TestGatewayRebindAttemptViewUsesTypedCheckpointAndActualPriorReceipt',
  'TestGatewayRebindAttemptViewAcceptsGrantedUpgradeCheckpointWithoutInitialStateRewrite',
  'TestGatewayRebindLegacyCompositionCurrentCensus',
  'TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists',
  'TestGatewayCurrentRouteOperationProviderAbsenceIsFailClosed',
  'TestGatewayRebindRuntimeCleanupRejectsUnownedResources',
  'TestGatewayRebindRuntimeCleanupStopsOnUncertainEffects',
  'TestGatewayRebindRuntimeCleanupRemovesOnlyExactResourcesInOrder',
  'TestGatewayRebindRuntimeLivePreconditions',
  'TestGatewayRebindRuntimeReplayCommandAudit',
  'TestLiveGatewayRebindRuntimeCommitAndReplay',
  'TestLiveGatewayRebindRuntimeRollbackAndReplay'
)
$pattern = '^(' + ($tests -join '|') + ')$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=10m -json ./cmd/hostd ./internal/generatedingress -run $pattern
```

Windows, Go 1.26.0: **17 top-level tests and 86 subtests passed**, zero failures,
exit 0. `cmd/hostd` completed in 1.127 seconds; `internal/generatedingress` in
106.249 seconds. Exactly the two named live Docker tests skipped for missing
explicit live opt-ins. These are discovery results, not real Docker acceptance.
Every expected top-level outcome, both package completions and the separate exit
file were checked. The process was reaped.

The JSON log is retained outside the checkout at
`C:/Users/huang/Documents/Projects/Rig/temp/m3-approved-integration-20261007.jsonl`.
SHA-256: `29A36C357F51CFEBDC30A007CC22B9366579719B09FA5A5F7E0526B3790E86D2`.
Companion `.tests` and `.exit` files record exact selection and process status.

Additional checks passed:

```text
go vet -mod=readonly -buildvcs=false -p=1 ./cmd/hostd ./internal/generatedingress ./internal/appaccess
go build -mod=readonly -buildvcs=false -p=1 ./...
git diff --check b813eff HEAD
```

The initial working-copy `gofmt -l` check listed all 17 changed Go files because
Git checked them out with Windows CRLF endings. A read-only check then fed each
exact `git show HEAD:path` blob to `gofmt` and compared the output bytes: all 17
canonical files matched. The checkout was not rewritten to manufacture a pass.

## Integration review and limits

The combined source retains preparation before ordinary deployment-effects
admission, fresh inspection after admission with the same Manager, dedicated
recovery markers, strict legacy/typed selection, and unavailable default physical
factories. The integration tests exercise these contracts together; no new serving
capability was enabled by the merges.

The check does not establish a live hostd process restart, actual Docker networking,
Linux race behavior, second-device LAN access, frontend behavior or the full
repository suite. No controller was restarted and no runtime credentials or
protected history were removed. External databases remain application-owned and
configured through scoped runtime secrets.

Next: obtain separate publication authorization and run the hosted Linux Docker
gates. Factory enablement and the remaining process/fault/LAN acceptance stay
dependent on actual evidence. Reverting implementation code must retain the SQL
and protected history; an older binary may safely refuse retained state.
