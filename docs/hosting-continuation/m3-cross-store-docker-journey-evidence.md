# M3 real Docker cross-store runtime journey

Status: implementation, scoped Windows verification, Linux test compilation and
source/security review passed. Real Docker acceptance
has not run. This branch has no publication, merge or deployment authorization.

Branch: `feature/hosting-m3-cross-store-docker-journey`.
Base: `b813eff283a2590cbdabd76fd1b9044ef36d0a6f`.

## Purpose

Exercise the concrete typed runtime coordinator against actual Docker resources,
the protected gateway store and SQLite in one disposable fixture. The existing
cross-store live gates cover read-only proposals and recovery of a prepared claim;
they do not establish full runtime commit and rollback acceptance.

The new gates are independently reviewable from the pending startup-ordering and
legacy-source compatibility branches. They explicitly install the private runtime
drivers in the test fixture. Production factory activation, administrator API/UI,
hostd bootstrap, real controller process death, and legacy-source Docker acceptance
are outside this test unit.

## Invariants and verification plan

- Use the existing disposable Linux fixture with a LAN application and a
  loopback-only application. Real Docker performs all physical effects and
  observations; no simulated inventory or probe override supplies acceptance.
- Commit must retain immutable source evidence and grants, finish the typed
  protected history and SQL transition, release the fence, and serve the correct
  applications through the successor. The original LAN address must be withdrawn.
- Rollback is injected at the final protected commit append, before its write.
  Physical reconciliation remains the real runtime implementation. It must restore
  the original authority and reach a durable rolled-back outcome without successor
  resources remaining.
- A fresh Manager reuses the same actual Docker resources and repository. Public
  recovery must reattest the terminal state without mutating retained history or
  dispatching Docker mutations. This is an in-process fresh-instance replay claim.
- Test teardown must require exact protected and physical ownership. Unknown
  resources, history drift, foreign consumers or uncertain command outcomes must
  retain resources and fail the test. Broad label inventories detect residue only.
- CI runs each live test on a separate disposable Linux runner. Fixed fixture
  identities prohibit parallel execution on the same Docker daemon.

The hosted job requires `RIG_RUN_LIVE_GATEWAY_V2=1`,
`RIG_RUN_LIVE_GATEWAY_REBIND_CROSS_STORE=1`, and the additional full-runtime opt-in
`RIG_RUN_LIVE_GATEWAY_REBIND_RUNTIME=1`. Its exact selected test must produce one
pass event and no fail or skip events. The always-run resource census must be empty.

## Baseline evidence

On 2026-10-07, Windows with Go 1.26.0:

```text
go build -mod=readonly -buildvcs=false -p=1 ./...
```

Passed, exit 0, before new test files were added.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=5m -json ./internal/generatedingress -run '^TestLiveGatewayRebindCrossStore(ProposalRetainsCurrentAuthority|PreparedClaimProcessRecovery)$'
```

The package compiled and exited 0 in 1.236 seconds. Both selected existing live
tests were discovered and skipped because the live Linux opt-ins were absent.
These skips are compile/discovery evidence only.

The installed Docker CLI could not connect to
`npipe:////./pipe/dockerDesktopLinuxEngine`; the named pipe does not exist. No
Docker Engine version or live runtime result is established locally. Linux
iproute2 and disposable host interfaces are also required by the live fixture.

## Local change verification

Final Windows command on 2026-10-07, Go 1.26.0:

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=5m -json ./internal/generatedingress -run '^(TestGatewayRebindRuntime(Cleanup.*|LivePreconditions|ReplayCommandAudit)|TestLiveGatewayRebindRuntime(CommitAndReplay|RollbackAndReplay))$'
```

Exit 0, package 1.411 seconds. Exactly these five top-level tests passed, with
30 passing subtests and zero failed events:

- `TestGatewayRebindRuntimeCleanupRejectsUnownedResources`
- `TestGatewayRebindRuntimeCleanupStopsOnUncertainEffects`
- `TestGatewayRebindRuntimeCleanupRemovesOnlyExactResourcesInOrder`
- `TestGatewayRebindRuntimeLivePreconditions`
- `TestGatewayRebindRuntimeReplayCommandAudit`

Both `TestLiveGatewayRebindRuntimeCommitAndReplay` and
`TestLiveGatewayRebindRuntimeRollbackAndReplay` were discovered and skipped for
missing explicit opt-ins. The package pass does not turn those skips into live
acceptance. Each expected top-level pass/skip and the final package pass were
checked in the JSON log; its companion exit file contains `0`.

Log retained locally at
`C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-docker-final-20261007.jsonl`.
SHA-256: `23C3568C782613C00CB466F5D4872BBCC3F603B640D63F0011D43C596601D289`.

Other final checks passed:

```text
go vet -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress
go build -mod=readonly -buildvcs=false -p=1 ./...
gofmt -l internal/generatedingress/gateway_rebind_runtime_live_test.go internal/generatedingress/gateway_rebind_runtime_cleanup_test.go internal/generatedingress/gateway_rebind_runtime_cleanup_live_test.go
git diff --check
```

The formatter produced no paths. The all-package build is read-only; this scoped
test/CI change does not modify embedded frontend assets. The repository's broad
`make test` and frontend/production-asset build were not run for this test-only unit.

Linux/amd64 test compilation also passed, exit 0, with `GOOS=linux`,
`GOARCH=amd64`, and `CGO_ENABLED=0`:

```text
go test -mod=readonly -buildvcs=false -p=1 -c -o C:/Users/huang/Documents/Projects/Rig/temp/m3-runtime-docker-linux-20261007.test ./internal/generatedingress
```

The binary was not executed. This establishes Linux compilation only.

The edited workflow parsed successfully with the installed PyYAML 6.0.2 parser.
Read-only configuration assertions found exactly four distinct matrix tests,
all three live opt-ins, `fail-fast: false`, and the always-run cleanup census.
This verifies YAML/configuration shape, not execution by GitHub Actions.

### Review and physical-proof limits

Orchestrator and independent security review returned a scoped source GO after
the initial draft's live-only assertion and cleanup gaps were corrected. Final
review covered fixture-bound intent/checkpoint selection, complete protected-file
pins, SQL/current-state/runtime-head stability, initial and stopped physical
attestation, exact owned-stop dispatch, name and retained-ID absence, complete
owned-resource census, volume/network metadata and consumer checks, and the exact
next-command guard at each effect boundary.

Cleanup reuses production physical and ownership validators. Its application
network proof excludes only the terminal-bound final gateway whose removal is
intended; all other observed members, aliases and healthy application endpoints
remain in the proof. Coordinator observations remain actual Docker observations.
The successful teardown path and its live adapter are still unexecuted locally;
the deterministic tests exercise the shared ordering/denial boundary.

The final working-file SHA-256 values are:

| File in `internal/generatedingress/` | SHA-256 |
| --- | --- |
| `gateway_rebind_runtime_live_test.go` | `0EB6725502EAA1E403E53D992BA7DE6B632774F81283B75A85FEFFD6439D1B58` |
| `gateway_rebind_runtime_cleanup_test.go` | `95BC060171340BA81336CDC8AA863E3C85076ABCD233A4567C75D3C1A4A746D0` |
| `gateway_rebind_runtime_cleanup_live_test.go` | `0F6E80A3FF1B1ECAE11B6FD585E9A06AE2A22C5007BE92761DD61A576617A7DC` |

## Remaining acceptance and rollback

Actual Docker commit, rollback, fresh-instance recovery, route behavior and complete
fixture cleanup require the hosted Linux gate. Hosted execution requires separate
publication authorization. No hosted run is claimed here.

The change is test and CI scope only. Reverting its commit removes the new acceptance
cases without changing production runtime behavior, data formats or existing history.
No full repository test suite, frontend suite, Linux race run or hostd startup
acceptance is claimed by the scoped local checks.
