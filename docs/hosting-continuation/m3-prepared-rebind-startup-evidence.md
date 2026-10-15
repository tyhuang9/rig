# M3 prepared rebind startup repair

Status: reviewed candidate for hosted CI, 2026-10-15. The supported Docker
crash matrix and M3 acceptance remain pending.

## Failure and scope

The prior supported-controller matrix reported `startup=upgrade_history` at
rollback boundaries and lost cutover acknowledgment. A local regression
reproduced the handoff failure: recovery had produced a terminal ABORT and
released its SQL fence, but ordinary startup rejected the retained protected
rebind history. Relaxing only that initial classifier would still leave the
next LAN-batch inspection and `Recover` / `Provision` calls unable to select
the native gateway.

Production commit `cf494137668d97763e34b1b53b1be61fa053f987` repairs that
handoff and the downstream selection. It is integrated with draft #170's
bounded 63-case CI matrix at `bb03107c8024d1573ee9580ec00a1f0ffc6c877f`.
All 22 reviewed production/test files are unchanged by that integration.

## Invariants

- Ordinary startup retains its strict history reader. A separate prepared
  entry point rebuilds and compares the supplied proof under the existing
  gateway lock before classification and before successful return.
- Every protected rebind attempt must have a validated terminal receipt.
  A previously completed attempt does not hide a later unresolved tail.
- Native operation after ABORT requires fresh SQL-selected native authority,
  released fence, no active attempt, complete all-ABORT history bound to the
  same lineage, no successor current-route bundle, and stable repeated reads.
  The fallback does not mutate SQL or protected history.
- Recovery can refresh its current-state proof after legitimate route/LAN
  state progress. Authority, full lineage, state version and immutable
  retained receipts remain pinned; revision rollback and same-revision
  digest drift are refused.
- Observation and LAN proof lookups use their existing bounded contexts,
  including the newly introduced SQL reads while the gateway lock is held.
  Cancellation returns an error and cannot invoke a success callback.

The shared reader now receives context from its callers. Most caller edits
only propagate their existing context; the observation and LAN resolution
sites also restore their intended timeout boundaries. There are no schema,
authentication, secret, managed-database, provisioning or deployment changes.

## Executed local evidence

Go 1.26.7 used `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`, and `CGO_ENABLED=0`.
The source was frozen before the following final runs:

| Check | Actual result | Raw output SHA256 |
| --- | --- | --- |
| Windows focused generated ingress | 16 parents passed, 242.517s; no failure/skip markers | `1c1c09fd76207a0cf3e58cc3c46b2c475e331923c49fbeceb03eaf68b6b871af` |
| Windows hostd startup/rebind | 23 parents passed, 5.411s; no failure/skip markers | `794efaf1c37bd5eebbb37a034a7df2bbd5e35eb19bd3871f0b6883943675c1cd` |
| Linux focused generated ingress, executed under WSL | Same 16 parents passed; no failure/skip markers | `6abf5d72435ae129464e37a1448b942421b6c65fd931be98f5db8301b7dfed27` |
| Linux hostd startup/rebind, executed under WSL | Same 23 parents passed; no failure/skip markers | `da025d48d6fb1f9fc803ee1f2ce87dc6e20eec5b2348ca3012eeedc0a8eabbca` |

All generated-ingress production/test callers compiled. `go vet
./internal/generatedingress ./cmd/hostd` passed with no diagnostics. Both
Linux binaries were cross-compiled and then actually executed; compilation
alone is not counted as execution. A further Windows hostd run after the
production commit passed the same 23 parents in 5.384s.

The generated-ingress selector was:

```text
^(TestGatewayV2StartupPreparedAbortRetainsOrdinaryRefusalAndAdmitsExactProof|TestGatewayV2StartupPreparedLostCutoverAckCommitDirectionAdmitsExactProof|TestGatewayV2StartupPreparedRebindRequiresFreshProofAfterRouteRecovery|TestGatewayNativeAbortAuthorityRejectsSQLAndProtectedDrift|TestGatewayNativeAbortAuthorityRejectsCommittedTerminal|TestGatewayNativeAbortAuthorityCallersBoundSQLReadsWhileLocked|TestGatewayV2StartupDispositionTable|TestGatewayV2StartupProofFailuresFailClosed|TestGatewayRebindCurrentStartupBindsSQLAndPhysicalAuthority|TestGatewayRebindCurrentStartupRouteRecoveryBeforeAdmission|TestCommittedV2ManagerSwitchObserveRecoverAndSnapshot|TestGatewayCurrentNativeAbortEmergencyRequiresCompleteBoundHistory|TestGatewayCurrentStateMachineReplaysTransferredGrantWithoutRawProfileRewrite|TestGatewayV2LANStartupBindsNativeProjectionBeforeAndAfterPhysicalProof|TestGatewayV2StartupClassifiesProtectedLANRecoveryBatch|TestHasGatewayV2LANRecoveryBatchSelectsIntentBeforeReload)$
```

Windows used `go test ./internal/generatedingress -run <selector> -count=1
-v`. Hostd used `go test ./cmd/hostd -run
'^(TestGatewayRebindStartup|TestGatewayStartup|TestRebindStartupMapping)'
-count=1 -v`. Linux binaries used the same selectors with `-test.run`,
`-test.count=1`, and `-test.v`.

Independent code and security reviews found no remaining issue in these 22
files after fixing incomplete-history, orphan-bundle and bounded-context
findings. Root verified the reviewed file hashes and all saved worker
artifact hashes. Review is not a substitute for the hosted runtime gates.

## Combined-source checks

After integration at `bb03107c8024d1573ee9580ec00a1f0ffc6c877f`, the exact
24-parent hosted preflight selector passed on both Windows and Linux, with
204 descendants and no failures or skips on either platform. Windows raw
JSON SHA256: `2db4051492712774a52ad8fb8ebaf74cf80fc0376b2a55eea843943b12f877bb`.
Linux raw output SHA256:
`e2e8de633127dcdbb1d06e14255d70e915ece163c796688a832a2c7ce64be5c8`.
The tagged Linux binary was actually executed under WSL from `cmd/hostd`,
matching the package working directory used by `go test`.

The matrix contract check, `pwsh -NoProfile -File
scripts/check-generation.ps1`, `go build ./cmd/hostd ./cmd/hostctl
./cmd/rig-relay`, and aggregate `git diff --check` all exited zero.

Two initial local WSL invocations failed: shell parsing of the grouped
selector, then source-inspection relative paths when launched from the
repository root. Their logs are retained. Explicit `--exec` and the package
working directory corrected the invocations without changing source; the
passing execution above is the final Linux result.

## Limits and rollback

The focused tests use real protected/SQL fixtures with controlled runtime
drivers. They exercise downstream `Recover` and `Provision` and actual
pending-route recovery. The R01/R02 labels share an already-ABORT fixture;
the lost-cutover label starts from an already-committed fixture. Those labels
do not establish the distinct Docker crash sequences. LAN refresh coverage
is a predicate table, not an entire post-composition LAN recovery run.

Local Docker and Linux race tooling are unavailable. Hosted CI must prove
all 63 supported-controller cases, cleanup and the final-source race suites.
No running local controller was refreshed, no deployment occurred, and no
GitHub target merge was performed.

Reverting the production repair restores the prior conservative refusal.
Keep SQL and immutable protected history intact; deleting retained evidence
is not a rollback procedure. A revert can therefore restore startup refusal
after a terminal ABORT until a corrected binary is available.
