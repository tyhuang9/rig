# M3 combined local acceptance evidence

Recorded 2026-10-10. Branch: `feature/hosting-m3-combined-acceptance`.
Status: approved local integration and selected behavioral/static checks passed.
The combined candidate has not run hosted Docker or race CI. M3 is not accepted.

## Scope and immutable inputs

The user approved local integration and verification of these exact inputs onto
published PR #137 head `788c508111f62830c3fa27b40feb4e5b0a33036b`:

| Input | Purpose | Local merge commit |
| --- | --- | --- |
| `f351c9e93d72a59d83fb58efbb644b7e0c04e994` | Isolated migrated database fixtures | `2661b47cd08cd2bc2df6c70ac23b2e431c4c13de` |
| `16c79a4738a11f84dade52ff3932c57a244228ee` | Process recovery and transferred lifecycle | `7e0349d012c97f92e4b3e7d34e34d56c36823ced` |
| `d27c2178a068a8364fac3d1130d7865437e58ac9` | Predecessor retirement and reviewed fixture repair | `beb3e316b0d95f93e1cc48f2b420d340f62046b5` |

The three ordinary merges preserved history. All four inputs are ancestors of
tested source HEAD `beb3e316b0d95f93e1cc48f2b420d340f62046b5`, whose tree is
`60b65e33334e48ca87a1372c312a170db06552d6`. The 290 pre-existing refs remained
unchanged. The evidence page is an additive documentation change after that
tested source commit.

The union contains 45 distinct changed paths from common base
`f49cf793bfae06f3d28fa45d4609a9489075cccb`, or 42 beyond `788c508`, before this
page. Forty-three single-owner paths retain their exact input blobs. Exactly
two test files overlap between recovery and retirement:

- `internal/generatedingress/gateway_rebind_multigeneration_backend_test.go`
- `internal/generatedingress/gateway_rebind_runtime_composition_test.go`

Both auto-merged without textual conflict and received semantic review against
both input tips. The union retains strict command target parsing, subsequent
live-config observations, immutable native-stage identity, cancellation,
the narrowly defined zero-claim census, exact TAR/config/autosave behavior,
strongest uncertainty diagnostics and exact owned cleanup/history assertions.

There are no CI, module, schema or production effect-factory activation changes.
External databases remain application-owned; no managed database provisioning is
added. Authorization covers this new local branch only. Publication, GitHub
merging, deployment and controller activation are separate actions.

## Selected combined behavioral verification

Go 1.27.0 was used with `GOTOOLCHAIN=local`, readonly modules,
`-buildvcs=false` and `CGO_ENABLED=0`. Windows-built Linux test binaries were
actually executed under WSL, with a clean environment and each package's
working directory. Native checks executed Windows test binaries. At most two
Go commands ran concurrently.

For each row, the harness compiled its package using `go test -c -o ...`,
listed the anchored exact selection using `-test.list`, and executed it using
`-test.v -test.count=1 -test.timeout=30m -test.run`. The retained command manifest
contains every actual argument, environment, selection and binary path.
The `.test.exe` suffix on the WSL artifacts does not change their Linux target.

| Actual execution | Parent executions | Leaf subcase executions | Outcome |
| --- | ---: | ---: | --- |
| Generated ingress, WSL | 20 | 42 | PASS; run wall time 302.344 s |
| hostd, WSL | 8 | 83 | PASS; run wall time 2.781 s |
| hostd, Windows | 8 | 83 | PASS; run wall time 2.610 s |
| Generated ingress, Windows | 7 | 12 | PASS; run wall time 20.609 s |
| Database fixture and four repository packages, WSL | 8 | 14 | PASS across five package binaries |
| Total, including platform repeats | 51 | 234 | No failures, skips or panics |

All nine compile/discovery/run triplets exited zero, with exact expected parent
and leaf inventories. These are execution counts, not unique tests across
platforms. All 791 Go/module source pins matched before and after execution.
The QA wrapper was collected with exit zero. No source repair was needed.

### Selected parent inventory

The WSL generated-ingress selection was:

```text
TestGatewayRebindStageConfigurationDigestPreservesFinalProgress
TestLiveCrossStoreAdmissionChildInputJSONRoundTripPreservesPreparedInspection
TestGatewayRebindAdmissionProcessRejectsBoundaryWork
TestGatewayRebindAdmissionProcessSerializesContenders
TestGatewayRebindAdmissionProcessRecoversCommittedClaim
TestGatewayRebindAdmissionProcessRecoversProtectedPreparation
TestPrepareGatewayRebindLockedIntentBoundaryRetainsFence
TestGatewayRebindConcreteCompositionTransferredLANLifecycle
TestGatewayRebindMultiRunnerCurrentCommandRefusal
TestGatewayRebindStartupRetiresReturnedNativePredecessorBeforeRestoringCurrent
TestGatewayCurrentPredecessorSharedListenerAcceptsExactSelectedMarkerProjection
TestGatewayCurrentServingRestoreRetiresPredecessorsBeforeServing
TestRetireGatewayCurrentPredecessorsStartupUsesSelectedRebindAuthority
TestGatewayCurrentPredecessorRetirementBuildsCompleteChainBeforeEffects
TestGatewayCurrentPredecessorRetirementAcceptsCanonicalLegacyLineage
TestManagedGatewayCurrentPhysicalDriverReconcilesOnlyExactSelectedMarker
TestManagedGatewayCurrentLANCommitRecoveryPhysicalAuthority
TestGatewayRebindLegacyCompositionRetirementCensus
TestGatewayRebindConcreteCompositionLegacySource
TestGatewayRebindConcreteCompositionCommitsAndReplaysSameDockerState
```

Both platforms executed these hostd parents:

```text
TestGatewayRebindAdmissionProcessSerializesHostdContenders
TestGatewayRebindStartupActiveResultAndLeaseOrdering
TestGatewayRebindStartupDispatchPreservesDedicatedRecovery
TestGatewayRebindStartupFreshDatabaseAndManagerReuse
TestGatewayRebindStartupRefusesUnverifiedRecovery
TestGatewayRebindStartupRetirementPinsEveryReboundMode
TestGatewayRebindStartupRetirementRequiresExactCurrentKind
TestGatewayRebindStartupVerifiesActiveResultBeforeRetirement
```

Windows generated-ingress execution repeated the four AdmissionProcess parents
and `TestPrepareGatewayRebindLockedIntentBoundaryRetainsFence`, then added:

```text
TestGatewayCurrentPredecessorRetirementFailurePreservesStrongestUncertainty
TestManagerGatewayCurrentPhysicalRuntimeStopsExactRetirementTarget
```

The WSL capacity selection comprised:

| Package | Exact parents |
| --- | --- |
| `internal/testsupport/databasefixture` | `TestOpenMatchesFreshProductionDatabase`, `TestOpenConcurrentClonesAreIndependent`, `TestOpenRefusesExistingControlArtifacts`, `TestOpenRejectsControlDatabaseSymlink` |
| `internal/appaccess` | `TestGatewayProfileApprovalCASReplayAndHostAddressBoundary` |
| `internal/autodeploy` | `TestConfigureIsDefaultOffOwnerScopedAndRetiresLastSubscription` |
| `internal/controller` | `TestDeploymentMutationsRequireAuthenticationAndCSRF` |
| `internal/controllerrelay` | `TestIdentityAndKeyRepositoryEnforcesCanonicalCASState` |

### Behavioral receipt identity

Artifacts are retained outside the checkout under
`temp/m3-combined-acceptance-20261010/verification/`. The durable source and
inventories above remain available without that local artifact directory.

- `combined.command.json`: complete selection, source pins and launch plan.
- `combined.result.json`: actual 27 command outcomes and nine strict assessments;
  SHA-256 `12c263c92d7e8bf3228f94b5284b5cae4c5d8b5f47897603dfe7faba88c37de2`.
- `source-provenance.json`: the original launcher's `source_oid` field is the
  **tree** `60b65e3`, not the commit. The sidecar records both HEAD and tree;
  original receipts were preserved and tests were not rerun for this correction.

Actual aggregate run-log SHA-256 values:

```text
WSL ingress:     d69fb6fde6dc592dc49ec271995e0374392bb459b6ed7b5ca796ff4f95287317
WSL hostd:       0b1e011c8a05193c4e90e7b455ccb4ea4c9babcc8e2778bbd71d9063fd766a13
Windows hostd:   3562637f01c9615dbc24a688d564782f38832bafaf8b01f38f3ef2fe620d9a85
Windows ingress: a102521cd3b1839d3327736655f365a98b1d85a41157d2f3a4f9ef6656bc5a98
```

The five capacity run-log hashes and package wall times are recorded individually
in `combined.result.json` and the corresponding package run receipts.

## Static checks, builds and review

These commands exited zero against the same source:

```text
go vet ./internal/generatedingress ./cmd/hostd ./internal/testsupport/databasefixture ./internal/appaccess ./internal/autodeploy ./internal/controller ./internal/controllerrelay
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
go list -deps ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
git diff --check f49cf793bfae06f3d28fa45d4609a9489075cccb HEAD
```

Vet took 63.007 s and CLI builds took 45.267 s. The four production dependency
closures exclude `internal/testsupport/databasefixture`. Only the four intended
test fixture constructors import/use that helper. All 37 changed canonical Go
blobs passed formatting comparison. The 22 distinct selected generated-ingress
parents map exactly once into 18 aligned partitions across three workflows;
workflow blobs are unchanged from `f49cf79`. This is a selection audit, not a
hosted execution claim.

The original static wrapper exited one after its Go commands passed because
its metadata parser omitted digits in `gateway_v2_pattern`. The first parser
correction also failed on `gateway-v2`. Both failures remain retained. A final
assessment-only correction passed without repeating the Go commands:
`static/final-assessment.json`, SHA-256
`30a69d67fbd4a3b7d19cc0eddaebf1365736c061173f2dbecf83fad57a3f06c6`.

Independent source/security and code reviewers reported GO with zero blockers
at `beb3e31`/`60b65e3`; QA separately reported GO on the actual receipts.
CodeRabbit CLI was unavailable in WSL, so these are manual specialist reviews.

## Documentation verification boundary

The declared documentation dependencies installed successfully using
`pnpm --dir docs install --frozen-lockfile --offline` with Node v24.15.0 and
pnpm 11.22.0. The install took 3.391 s and left pinned inputs unchanged.
Install-log SHA-256:
`304f3251c006e579d3b0211d92baaf88bc41906f88e964d18bee9c83f466924b`.

After this page is frozen, the delivery checks are:

```text
pnpm --dir docs check:workflow
pnpm --dir docs build
pnpm --dir docs check:accessibility
```

Their actual command outcomes, log hashes and before/after input pins belong
to `temp/m3-combined-acceptance-docs-check-correction-normal-user-20261010/result.json`.
This page does not predeclare that subsequent result; final readiness requires
that receipt and final review of the frozen page.

## Hosted component observations, not combined acceptance

These observations were collected on 2026-10-10. They apply to the exact
published component sources below; the combined candidate remains unpublished.

### PR #137, projection repair at `788c508`

GitHub tested merge `e17253a8cf96024e5c6129f8323d3d14481ff553`; its tree
`533d3d750ac903f382f9e44bef505c64f5c25407` matches the component head exactly.
The [gateway run](https://github.com/tyhuang9/rig/actions/runs/38016790509)
completed with 13 successful and two failed jobs. The private Docker child
commit and rollback journeys passed (jobs `114108728978` and `114108728786`).

The typed runtime journeys still failed:

- Job `114108728935`, `TestLiveGatewayRebindRuntimeCommitAndReplay`: last
  validated sequence 9, `stage_start_intent`, with
  `route_reconciliation_required`; cleanup could not identify an exact terminal
  state and retained resources.
- Job `114108728867`, `TestLiveGatewayRebindRuntimeRollbackAndReplay`: last
  validated sequence 12, `rollback_intent`; forced sequence 17 was not reached.
  Cleanup could not acquire the gateway lock and retained resources.

The logged `unknown` command classification does not establish an invalid Docker
command or identify the first refusal. No root-cause repair is claimed here.
The [deployment run](https://github.com/tyhuang9/rig/actions/runs/38016790501)
also failed its repository job after appaccess/controller package timeouts;
this source does not include the capacity input. The
[Windows run](https://github.com/tyhuang9/rig/actions/runs/38016790522) failed
`TestLiveExternalTLSNodeTrust`: the host-side Node probe did not complete within
its five-second context. The log does not establish the TLS root cause.
Runtime and controller runs completed successfully.

Raw logs and assessments are retained under
`temp/pr137-788-hosted-acceptance-20261010/`. These negative observations remain
part of the acceptance record.

### PR #138, capacity at `f351c9e`

The [deployment run](https://github.com/tyhuang9/rig/actions/runs/37997312036)
passed. GitHub tested `736a2538468fff1bf910037a769d0398bd8d4996`, whose tree
`db9523628426723707716f0198489557650e06b9` matches `f351c9e` exactly.
Repository job `114046534216` passed appaccess (830.953 s), autodeploy
(102.883 s), controller (1758.329 s) and controllerrelay (84.981 s), without
race or timeout markers. Existing timeouts were unchanged. This establishes
component execution, not a comparative speedup or combined-branch race pass.
Raw-log SHA-256:
`b8916d157c58ecaec66ac205b2f52d2536ee003cfd28f9e5d69aa0967d961061`.

## Remaining acceptance and rollback boundary

The combined checks use real SQL/protected state and real child-process
termination/reaping where selected, with simulated physical adapters. They do
not establish actual Docker, Linux race, complete process-loss coverage,
returned-NIC behavior or second-device LAN acceptance.

The first uncovered protected progress write remains P2; the first typed Docker
effect gap is network creation before P3. Stages 2-12, final handover, terminal
receipt/baseline, SQL commit and fence/deferred-release boundaries still need the
full process-loss matrix. The two passing private Docker child exits do not
prove the full controller/SQL successor and worker-release lifecycle.

Next acceptance work must address the demonstrated typed Docker failures and
remaining crash/physical gates while preserving fresh authorization, immutable
ownership/history, uncertainty diagnostics and closed production effect
factories. No production controller was changed or restarted for this local
integration. Rollback consists of withholding this local candidate; existing
branches and deployed/controller state were not updated.
