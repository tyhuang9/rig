# M3 predecessor-retirement fixture compatibility

## Scope and observed baseline

This follow-up starts at PR #142 head
`cb388d429e35c0b82197567b5fce7595648602c8`. It updates six test files for
the predecessor-retirement contract added by that PR. Production checks,
immutable SQL/protected history, dependency pins and CI selections are unchanged.

The failing hosted Linux and Windows jobs checked out synthetic commit
`0abba024c0a329dcd191b1e8b56d8facf8c3660b`, whose parents are published
`f49cf793` and `cb388d4`. Its tree equals the local baseline tree
`96378b209df134dd0b76b059025e2f694b0ea91d`.
The [deployment run](https://github.com/tyhuang9/rig/actions/runs/38015319863)
and [Windows run](https://github.com/tyhuang9/rig/actions/runs/38015319930)
reported eight affected test families:

| Parent | Existing CI partition |
| --- | --- |
| `TestGatewayRebindCurrentAttestationCrossesSQLCommitBarrier` | `rebind-current` |
| `TestGatewayRebindTerminalAttestationWithdrawsOnlyRetainedOwner` | `rebind-history` |
| `TestRecoverGatewayRebindStartupCompletesDatabaseCommittedAttemptAndReattestsCurrent` | `ingress-remainder` |
| `TestGatewayRebindLegacyCompositionCurrentCensus` | `rebind-history` |
| `TestGatewayRebindConcreteCompositionLegacySource` | `rebind-composition-legacy-source` |
| `TestGatewayCurrentServingRestoreComposesSQLWithConcreteExecutor` | `current-runtime` |
| `TestGatewayRebindTypedTerminalAttestationPreservesCommittedHistory` | `rebind-typed-runtime` |
| `TestGatewayRebindConcreteCompositionCommitsAndReplaysSameDockerState` | `rebind-composition-native` |

The examined logs contain route-reconciliation failures without a race-detector
or package-timeout diagnostic. A separate second-generation job failed while
downloading SQLite before tests ran; that infrastructure failure does not
justify changing runtime source or test timeouts.

Five parents reproduced on the unchanged baseline with an actual WSL execution:
build and discovery exited 0, execution exited 1 after 104.560 seconds. The
SQL/concrete-serving parent then reproduced using the unchanged binary in
9.193 seconds, exit 1. The typed-terminal parent has immutable hosted failure
evidence. These negative outcomes are retained alongside subsequent checks.

Local raw evidence is under
`temp/m3-retirement-fixture-compatibility-20261009/`; hosted logs, checkout
metadata and hashes are under `temp/pr142-hosted-acceptance-20261009/` in the
workspace wrapper. These temporary artifacts are not repository dependencies.

## Fixture correction and retained assertions

The single-current executor fixtures model one selected container. Their
retirement adapter now verifies an exact action derived from the fixture's real
retained history, counts retirement invocations separately, checks authorization
before and after the action, and has explicit refusal and lost-authorization
controls. Existing current-container observation, cleanup, uncertainty, SQL
and history assertions remain active. The adapter models the retirement contract;
it does not claim to execute physical predecessor stops.

The completed-batch serving case precomputes the one permitted post-marker
action from frozen history, preserving the same owner and ancestor chain. Its
observation allowlist contains only that action and the original action. The
fixture advances its expected retirement action only after verifying the
persisted marker transition. Unrelated actions must be rejected.

After a selected-rebind recovery failure, production preserves the strongest
may-live uncertainty even when exact-current withdrawal succeeds. Earlier
terminal-withdrawal and native-composition expectations assumed that successful
withdrawal cleared that diagnostic. The correction requires the stronger
diagnostic while preserving separate checks for exact stop commands, actual
fixture liveness, blocked admission, SQL state and immutable history.

The legacy multi-generation backend now recognizes the exact native stage
container and reads its retained presence and metadata. An unknown name still
fails as an unowned command. Controls prove known absence, known presence,
selection refusal on stage reappearance, and unknown-name rejection. The
presence control establishes that presence is not converted into a not-found
result; it does not establish a complete valid positive stage identity.

Retirement can request a current host census without a new admitted claim.
The fixture returns only explicitly arranged current and next host candidates
for the exactly empty claim. Every nonempty claim still uses the claim-bound
planner, including malformed claims. Cancellation is preserved. Fresh-manager
replay carries the same fixture census into its concrete backend.

Two additional controls are selected by `rebind-history`:

- `TestGatewayRebindStrictPredecessorRetirementContractRefusesAndRechecksGuard`
- `TestGatewayRebindLegacyCompositionRetirementCensus`

The static audit parses the actual runner filters and both workflow matrices.
All ten parents map to exactly one of the 18 required partitions in both Linux
race and Windows CI. Receipt: `root-ci-selection-ten.json` in the local evidence
directory. This mapping is separate from compiled discovery and execution.
The earlier nine-parent receipt remains preserved as `root-ci-selection.json`.

## Verification record

The first candidate failed compilation because the new census control omitted
the `sort` import. It ran no tests. Its command, source pins and build failure
remain under the `nine-parent-final-green` prefix. The import-only correction
uses a separate `nine-parent-final-green2` prefix and fresh execution evidence.

The corrected nine-parent run compiled and discovered all selected parents.
Build and discovery exited 0; actual WSL execution exited 1 after 140.991
seconds, with all captured Go/module hashes unchanged. Seven parents passed,
including legacy commit and rollback. Two still failed: the completed-batch
serving restoration subcase and five terminal-withdrawal subcases. The stable
serving subcase and two other terminal-withdrawal subcases passed. These partial
results do not establish acceptance. Native and static steps were not reached.
The retained WSL output SHA-256 is
`A496ACF99BBCD05942A4C121F03AC686BCEA6CC40EF1353D8237C38DF2F65B3E`.

The next frozen run, `seven-parent-final-green3`, exercised the affected
shared-helper users and the additional native-composition parent. Build and
discovery exited 0; actual WSL execution exited 1 after 59.277 seconds, with
unchanged captured Go/module hashes. Six parents passed, including completed
batch restoration and native composition. Only the missing-census and
post-attestation SQL-drift terminal subcases still failed. Their new expectations
had incorrectly used physical-observation counts as retirement counts; source
ordering requires one retirement attempt in both rows. The final correction
removes those two overrides without changing any helper or production guard.
The original seven-parent failure remains retained, with WSL output SHA-256
`966270713D7F4C3712C2F2BCC1E699F4EA86B6DFDCD7D184F2ECD7C5ACED4386`.

### Final focused acceptance

The final `terminal-native-final-green4` wrapper completed with observed exit 0.
Build, compiled discovery, actual Linux execution, native Windows discovery,
native execution, vet and formatting all exited 0. All 785 captured Go/module
input hashes remained unchanged, and an independent assessment matched the
current files to those pins and verified the raw-log hashes and exact test sets.

| Check | Observed result |
| --- | --- |
| Final terminal-withdrawal parent through WSL | 1 parent and 7 subcases passed; 7.874 seconds |
| Native Windows representative gate | 4 parents and 5 subcases passed; 231.282 seconds |
| `go vet ./internal/generatedingress` | Exit 0; empty output |
| `gofmt -l` on the six changed Go test files | Exit 0; empty output |
| `git diff --check` in the repair checkout | Exit 0; only ordinary line-ending notices |

The native selection covers SQL/concrete serving restoration, typed terminal
attestation, strict retirement refusal/guard/foreign-action controls, and the
legacy retirement census. It uses `go test -json -count=1 -timeout=10m` with
those four exact parent names. The final Linux command builds the package test
binary with `go test -c`, then executes it in WSL with
`-test.v -test.count=1 -test.timeout=20m` and the exact terminal-withdrawal parent.
The complete commands, explicit discovery sets and source pins are retained in
the final command/result JSON files. The local toolchain was Go 1.27.0 with
read-only modules and CGO disabled; hosted CI uses Go 1.26.7.

The scoped Linux evidence reconciles ten unique parents and sixteen subcases:

- Three unchanged legacy/census parents and two subcases from `green2`.
- Six passing dependent/composition parents and seven subcases from `green3`.
- The corrected terminal parent and seven subcases from `green4`.

Only three nonlegacy test paths changed between the first two runs. The final
delta changes only the terminal test's call-count assertions and diagnostic
message; its shared helper is unchanged. This is reconciled focused evidence,
not a passing whole-package run at one revision. Both earlier failed whole
runs remain failed in their original records. No passing legacy journey was
rerun solely to replace that evidence.

Final result SHA-256:
`76093598912503A078A20FAD76449AAC3C82EAAFD51782D6B99D2C5E9ECEEDA9`.
Final Linux output SHA-256:
`F7BA35B9E3A2266D8A1B2C588603A767CAADA5A9CB8233F65B2D5FD6446C35D7`.
Final Windows output SHA-256:
`FAB04BA36D808E55AE1A4A0C223791490E259E4A13C4EE3B0122FFAE830EF24B`.
Independent assessment: `root-final-assessment.json`.

### Documentation and review

Documentation dependencies installed successfully with the frozen lockfile and
offline cache, preserving all captured inputs. An earlier sandbox attempt
failed before installation when pnpm's version lookup could not fetch its
declared executable; that failure is retained separately.

The final documentation source is checked with the repository's declared gates:

```text
pnpm --dir docs check:workflow
pnpm --dir docs build
pnpm --dir docs check:accessibility
```

The publication proposal records the observed outcomes after this source is
frozen. Receipts, command exits, log hashes and before/after documentation pins
are retained under
`temp/m3-retirement-fixture-compatibility-docs-check-normal-user2-20261009/`.
This command list alone does not establish a successful documentation build.

Independent source/security review and aggregate inspection covered the strict
action allowlist, retained cleanup assertions, exact legacy dispatch and the
final minimal assertion correction. CodeRabbit CLI was unavailable; no
CodeRabbit assessment is claimed. Local review and targeted execution do not
replace the remaining hosted gates.

## Remaining acceptance and integration boundaries

Actual Linux race and Docker behavior require hosted checks on the corrected
source. WSL plain tests and simulated Docker executors do not establish physical
network changes or access from a second device.

This branch does not include PR #137's separately published stage-projection
repair at `788c508`, the capacity branch, or the recovery stack. The older
physical Docker failures ran source without `788c508`; they cannot establish
acceptance or rejection of that repair. The private final-handover diagnostics
match its motivating stage-projection problem. The typed commit/rollback
failures still need actual execution to establish their cause.

The future combined baseline has a shared test-backend path with PR #141.
Any authorized integration must preserve that branch's strict command dispatch
and current configuration projection together with this branch's exact native
stage inspection and candidate census. Combined regression execution is required.

Reverting this test-only follow-up restores the earlier fixture failures and
does not alter production behavior. Preserve the original negative evidence.
Publication and local integration remain subject to their explicit approvals;
no merge, controller activation or deployment is part of this change.
