# M3 supported rebind crash-matrix CI partitions

Status: candidate CI repair, 2026-10-15. This records the execution contract
for the hosted Docker crash matrix. It does not record a passing hosted run or
complete M3 acceptance.

## Why the matrix is partitioned

The former single hosted job ran all 33 durable-boundary cases and 30 scenario
cases serially. GitHub job `116532148446`, requested from PR head
`2a5812f21a81d085288ffd702903c835995bb04d` and checked out at
`6c7cebc28f7fc40fabb99d6713c4db704e55fb03`, reached the 345-minute package
deadline after 33 boundaries and 13 scenarios had begun. The observed serial
duration is larger than the 345-minute package, 350-minute step and 360-minute
job limits. Each case remains serial within its owner because it owns host
networking and a full hostd process, but independent hosted runners can own
disjoint groups.

`scripts/check-supported-rebind-matrix-contract.json` assigns the source
manifest's 63 exact cases to eight groups. Every group has at most eight cases:

| Group | Cases | Focus |
| --- | ---: | --- |
| `partition-01` through `partition-04` | 8 each | 30 durable boundaries and two scenarios |
| `partition-05` | 8 | Response-loss boundary and seven early lost-ack effects |
| `partition-06` | 8 | Both rollback boundaries, lost cutover, terminal replay and related recovery |
| `partition-07` | 8 | Pending recovery, authority, cancellation and ownership cases |
| `partition-08` | 7 | Drift, release, emergency-withdrawal and background-isolation cases |

The contract reads both closed required-ID lists and both implemented source
lists from `cmd/hostd/gateway_rebind_live_manifest_test.go`. It fails if any
manifest ID is missing, duplicated, unknown or differently implemented. It
also fails if the groups omit, overlap or add a case, or if the workflow's
inline matrix differs from the contract.

## Per-group proof and limits

Each group runs its cases serially with the existing nested Go selector:

```text
^TestLiveControllerGatewayRebindCrashMatrix$/^<boundary-or-scenario>$/^<exact-id>$
```

The selector has one literal name component for the parent, category and case;
it cannot select a sibling as an acceptable substitute. Each invocation keeps
`live_docker`, `-count=1`, verbose JSON events, the real runtime flags and the
existing `COMMIT`/`ABORT` assertions in the test harness.

Each exact invocation has a 30-minute Go package deadline: up to 20 minutes for
the observed live operation and a ten-minute cleanup reserve. The helper adds a
two-minute process-reap allowance and will not start a case unless its full
32-minute window fits before the 110-minute group deadline. The CI step has a
115-minute deadline and its job has a 130-minute deadline. All are below the
former 345-minute package, 350-minute step and 360-minute job limits.

The 30-minute case bound is based on the existing timeout evidence, not a claim
that these cases passed: `lost-network-create-ack` took 863.52 seconds from one
case start to the next, and `s09-effects-lease-release` took 695.469 seconds.
Other observed cases were at most about 489 seconds. The reserve covers rebind,
older-fixture and network cleanup plus child reap; it does not relax case
assertions or permit a runner to exceed its group deadline.

Before resources are created, every matrix job runs the existing 23
manifest/runner control parents and their 192 descendant checks. The job
retains the full owned-resource and child-process cleanup census under
`if: always()`. A failed case retains its individual JSON log and command exit
record in a seven-day failure artifact after cleanup executes.

The preflight also runs the helper's no-Docker self-test. It checks malformed
ownership, event streams and command failures, including a Linux parent that
exits while a same-process-group child holds the inherited output pipe. The
runner must kill that child without waiting indefinitely before the cleanup
census is allowed to run.

The event verifier requires exactly one `run` and `pass` for every selected
child, one parent `run` and `pass` per child invocation, and one package pass
per invocation. A missing event, unexpected sibling, failure or skip fails the
group. The aggregate job runs under `if: always()` and accepts only an overall
matrix result of `success`, so a failed, cancelled or skipped partition cannot
be hidden by successful controls in other partitions.

## Local contract evidence

On the candidate worktree, Python 3.12 ran:

```text
python scripts/check-supported-rebind-matrix.py --check-contract
python scripts/check-supported-rebind-matrix.py --self-test
```

Both commands exited zero. The self-test rejects a missing assignment, an
overlap, an unknown case, an unknown group, a command failure with retained exit
evidence, JSON skip/fail events and a missing JSON pass event. It does not run
Docker. The WSL verification used these exact commands; both exited zero,
including the Linux process-group timeout control:

```text
wsl.exe --cd C:\Users\huang\Documents\Projects\Rig\rig-hosting-m3-matrix-ci-partitions -- python3 scripts/check-supported-rebind-matrix.py --check-contract
wsl.exe --cd C:\Users\huang\Documents\Projects\Rig\rig-hosting-m3-matrix-ci-partitions -- python3 scripts/check-supported-rebind-matrix.py --self-test
```

The local no-Docker Go verification used Go 1.26.7 with
`GOTOOLCHAIN=local` and
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`:

```text
C:\Users\huang\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.7.windows-amd64\bin\go.exe test -tags live_docker -json -v -count=1 -timeout=2m ./cmd/hostd -run '^(TestGatewayRebindLiveRequiredManifestIsClosed|TestGatewayRebindLiveHarnessUsesSupportedDaemonAssembly|TestLiveHostdRebindDockerMutationClassifiesCopyDirection|TestAutoDeployStartObserverReportsOnlyEnabledLaunch|TestControllerRelayStartObserverReportsOnlyEnabledLaunch|TestGatewayRebindBackgroundLedgerAndDenyTransport|TestGatewayRebindBackgroundLedgerWriteFailureIsRetained|TestGatewayRebindBackgroundSentinelsUseRealRecoveryMethods|TestLiveGatewayRebindBackgroundLoopbackRejectedTLSRecordsAccept|TestLiveHostdRebindClosedDiagnosticControls|TestLiveHostdRebindLegacyV1OwnershipControls|TestGatewayStartupDiagnosticClosedCheckpointContract|TestGatewayStartupDiagnosticOuterStageWrappingPreservesCause|TestGatewayStartupDiagnosticRejectsForgedOrWrappedIngressMetadata|TestGatewayStartupDiagnosticWrapsRealStartupFailurePaths|TestGatewayStartupDiagnosticUsesDirectTaggedIngressResult|TestLiveHostdRebindClosedDiagnosticCheckpointRoster|TestGatewayStartupDiagnosticSnapshotStageAndCauseContract|TestGatewayStartupDiagnosticUsesDirectSnapshotResult|TestLiveHostdRebindClosedDiagnosticSnapshotCauseRoster|TestLiveHostdRebindPlanSuccessorProfile|TestLiveHostdRebindNormalReplayControls|TestLiveHostdRebindTerminalReceiptBindingControls)$'
C:\Users\huang\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.7.windows-amd64\bin\go.exe test -tags live_docker -run '^$' -count=1 ./cmd/hostd
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check-generation.ps1
```

The 23-parent control selector passed with no failures or skips. The tagged
compile exited zero with `ok github.com/hostd/hostd/cmd/hostd 0.671s [no tests
to run]`. The generation check exited zero and reported `ok
github.com/hostd/hostd/internal/controller 0.649s`. The local Windows host has
no Docker daemon, so the 63 real runtime cases remain pending hosted execution
on the exact published candidate.
