# Restore the predecessor after a failed activation write

Status: the rollback regression on [draft PR #260](https://github.com/tyhuang9/rig/pull/260) is corrected locally. The final focused suite passed eleven parent tests and thirty-four distinct cases with no failures or skips. Hosted verification of the correction remains pending; M4 is incomplete.

## Observed regression

The new retained-transition protocol must preserve ordinary replacement rollback when the activation database transaction is known not to have committed. The LegacyV1 rollback implementation currently accepts only a physical route already matching the previous state. It can clear a retained marker, but cannot restore the previous route when the proposed route is serving after a failed activation write.

The executor race job on PR #260 failed these existing checks:

| Test | Observed failure |
| --- | --- |
| `TestGeneratedExecutorCleansCandidateWhenActiveCASRollbackSucceeds` | One real activation call and two reloads; expected successful physical restoration adds the existing rollback effects. |
| `TestGeneratedExecutorPreservesBothSlotsWhenActiveCASRollbackFails` | One real activation call and two reloads; the intended rollback failure was never reached. |

The source-bound log is for job `124984874955`, which checked out merge `d409a92194c67c8c1acfe73ba7248c6c6f7e50a4` of head `3bcd9f80303760591a007816e1a5ac237770b3e9` into `8525d9cfc3eb089914157d4b6939724b6ccb355e`. Its decoded-log SHA-256 is `7a0ef540d10f24495f912b525ac93a5b0eab0355f9df32abc3e1f94e6351426a`. The same executor race job passed on PR #259, job `121822674928`. This is a regression to repair, not an assertion to weaken.

## Behavior

Failed fresh activation remains separate from cold previous-route marker clearing. A fresh activation may restore its exact predecessor only after current durable evidence proves the activation did not commit, the expected predecessor head remains current, and the current worker claim and execution binding still authorize the effect. A historical receipt or retained effects lease cannot substitute for current authority.

For an exact replacement transition whose proposed route is physically serving, restore the previous live and restart configurations through the guarded path, verify the exact physical result, and only then clear the retained transition. If physical restoration or readback is uncertain, retain conservative state and both capacity charges. Never terminalize or clean the candidate because of an uncertain activation result.

A retained Pending marker keeps the public route observation unresolved. Direct inspection may still identify the physical route; that does not authorize the public observer to report a reconciled route. The failed-restore regression must verify this public refusal alongside exact protected state, physical configuration, immutable activation history and retained capacity. Its original restoration-effect count remains required.

An already-restored exact previous route may reconcile idempotently. Mixed, missing, foreign, changed or ambiguous state must refuse effects. A committed/self head, crossed head, unreadable database, stale worker, durable cancellation or crossed binding must not authorize restoration. Caller-context cancellation alone may still allow restoration after the durable authority check succeeds. First-deployment recovery without a durable predecessor keeps its existing first-slot protection.

NativeV2 and CurrentV1 keep their existing physical restoration paths and CurrentV1 revision checks. The strict Previous-only cold-clear path never restores a physically serving proposed route.

## Verification

The unchanged `3bcd9f80303760591a007816e1a5ac237770b3e9` baseline reproduced both original failures with their assertions intact: successful rollback reported two reloads instead of four, and injected failed rollback reported two instead of three. `TestGeneratedExecutorPreservesFirstSlotWhenActiveCASFails` passed separately using the same compiled binary. Discovery found exactly those three parents. The binary SHA-256 is `0b02bd4be02ecdc7d2a687f2556877a2a6fb6ca8f01fdf3d14544c83a65ba95e`.

The baseline audit verified the original precompile TSV hashes against eleven source copies and the committed parent, compiler and binary identities, output hashes, sequential receipts, and the exact failed and passed parents. The JSON manifest and source copies were created after compilation; they are not represented as a precompile freeze. The audit SHA-256 is `4aa5f9c0f4f42ef32d3df90ae2f540925cc2586e44251f7ecb0047655f773510`. This establishes the baseline only.

The final passing source was frozen before compilation in a fifteen-path manifest, SHA-256 `185a898e7318205375c78d2e39cf2677eca5a45ec2b557bec1e61290b9972e26`, with content composite `11ac5a77b795e683341ee6acbe5613e797fc47d5da910c665e6db1aac4e913b5`. Exact discovery found three executor parents and eight ingress parents; all eleven parents and twenty-three child cases passed. The roster SHA-256 is `c81a8fbc74b3f16ef4ce3ba8993b617f739ec79d2fbeb2e8574f512ccfae7537`. Earlier failing repair and fixture runs remain excluded from acceptance.

The executor checks exercise the real activation path through successful restoration, uncertain restoration and first-deployment protection. The original successful and failed restoration counts remain four and three. The uncertain case now requires empty public observation plus `RouteUnresolved`, exact retained protected state, matching live/restart successor configuration, unchanged SQL head and receipt count, both running containers, and no cleanup or extra capacity admission.

Ingress checks cover exact proposed restoration, previous-state replay, malformed and mixed projections, failed restoration, cold Previous-only clearing, NativeV2 rollback and CurrentV1 durable-pending behavior. The missing-head real-entry test proves refusal **before** physical switching. Separate, explicitly named resolver controls create an authentic Pending transition, change durable attempt/cancellation/head state, then invoke the failure resolver directly; they establish that boundary's refusal and preservation behavior, not a full post-publication deployment journey. Caller-only cancellation and crossed-binding cases also exercise that resolver boundary.

Pinned Go 1.26.7 uses `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`. The local evidence compiles each package with `go test -c`, discovers the exact roster with `-test.list`, and executes each binary with an anchored `-test.run`, `-test.count=1`, `-test.v`, and `-test.timeout=5m`. Portable reproduction commands for the same selected tests are:

```sh
go test ./internal/generatedexecutor -v -count=1 -timeout=5m -run '^(TestGeneratedExecutorCleansCandidateWhenActiveCASRollbackSucceeds|TestGeneratedExecutorPreservesFirstSlotWhenActiveCASFails|TestGeneratedExecutorPreservesBothSlotsWhenActiveCASRollbackFails)$'
go test ./internal/generatedingress -v -count=1 -timeout=5m -run '^(TestCommittedV2RecoveryRollsBackDurableProposedRoute|TestGatewayCurrentStateMachineSwitchAndRecoveryUseDurablePending|TestGeneratedRouteActivationLegacyRollbackRestoresExactPredecessor|TestGeneratedRouteActivationPreviousExactLegacyClearMatrix|TestGeneratedRoutePredecessorRollbackAuthorityHandlesCallerCancelAndCrossedBinding|TestGeneratedRoutePredecessorRollbackAuthorityHelperRefusesDurableDrift|TestGeneratedRoutePredecessorUnreadableHeadRefusesBeforePhysicalSwitch|TestGeneratedRoutePredecessorUpdateFailureRollsBackAndRestores)$'
```

This is a focused local suite, not a full repository run or Linux race/Docker acceptance. The separate retirement-fixture, created-candidate restart, gateway-readiness, migration-reconstruction and LAN-restart failures remain outside this correction.

All ten native stages passed: two compiles, two exact-discovery checks, two test executions, and four static/build checks. Independent receipt audit SHA-256: `6d02e02a7ca9c129d59b996f484b3d94703ed6b2edb47da30af6d23858ba3a65`. Read-only `gofmt -d` was empty for all five changed Go files. These commands passed:

```sh
go vet ./internal/generatedingress ./internal/generatedexecutor ./cmd/hostd
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
go run ./cmd/openapi-gen -check
```

The separate controller contract `TestOpenAPIContractMatchesRegisteredRoutes` also passed, outside the eleven-parent/thirty-four-case suite. Its compile, exact discovery and successful package-directory execution bring the accepted native-stage total to thirteen. Audit SHA-256: `a7216d17a3cc5c80529433eb1a984ecbd3d6cba015d8cfa5e8059aa95a195127`. An initial invocation from the repository root failed only to find the relative `../../api/openapi.yaml` input and is excluded; the same binary passed from `internal/controller` without source changes.

The documentation gate uses the frozen-lockfile install, workflow contract check, production build and accessibility contract check in `.github/workflows/pages.yml`. Local installation uses the cached offline dependencies. Final documentation receipts and the current-source closure are recorded in the pull request evidence.

## Rollback and scope

This correction introduces no schema or public API change. Reverting it restores the current refusal to restore a proposed LegacyV1 route after an activation write failure; preserve all immutable history and retained capacity. The inherited [activation recovery restrictions](m4-recovered-route-activation.md) still apply to downgrades with retained transitions.

External databases remain application-owned through scoped runtime secrets. No managed database or Neon provisioning is added. Draft publication does not authorize a GitHub merge, deployment or live-controller refresh.
