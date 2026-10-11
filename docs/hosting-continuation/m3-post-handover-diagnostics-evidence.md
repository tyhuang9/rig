# M3 post-handover failure-state diagnostics

Status: focused native behavioral verification and static checks passed; documentation verification is recorded in the external receipt below. This is test-only diagnostic work, not a runtime repair or M3 acceptance.

## Source and observed failure

The local branch starts at published PR145 head `457538ad08867c5e982de5a93f29a1402fe30bdf`. Its actual hosted test merge was `07874a5040d60d0cb7b2e736d1ef450b6e5d805d` with the same source tree.

Gateway workflow `38112643326` finished with 14 successful jobs and one failed job. Typed rollback job `114391210875` passed in 162.91 seconds and independent cleanup succeeded. Typed commit job `114391210905` failed in 143.46 seconds after durable sequence 17, `handover_committed`. Its recorded stage methods succeeded; the latest container inspection succeeded with an active context. The public refusal remained `route_reconciliation_required`.

The failure log does not identify which post-handover coordinator boundary refused. Cleanup reported that it could not acquire the gateway lock; this alone does not establish a leaked mutex. Raw evidence is retained outside the repository at `temp/pr145-457-hosted-acceptance-20261010/job-114391210905-response.json`, SHA256 `F27B895A04405FBF572E528329A204164A99877A3CF532649AD7DC5DC5FDAEC6`.

## Scope and invariants

The change is confined to the live test's existing driver decorator, failure logger, and a test-only diagnostic helper. It records finite outcomes at the existing `reconcile_successor`, `confirm_forward`, and `current_attestation` delegation boundaries, preserving the original arguments, results, and errors. The earliest failed boundary remains latched, including unknown classifications. Successful boundary observations are retained independently.

These are outer decorator boundaries. An internal call made directly by the concrete driver is not separately intercepted; a reconciliation refusal does not identify which internal predicate refused. The complementary durable snapshot reports stable SQL phase, selected SQL authority, and validated artifact presence for this fixture operation. Artifact labels do not establish complete protected lineage, an exact route baseline, or physical serving authority.

The original runner and method diagnostics are captured before diagnostic reads. One five-second child context inherits the actual journey's cancellation and any earlier deadline. It bounds lock/SQL waits and invalidates results after cancellation; synchronous filesystem validation uses the existing read-only loaders.

The snapshot uses the existing inspection lock that permits observation while fail-stop is latched. It grants no effect authority. SQL and protected artifact reads must be stable before classification. Read failure, cancellation, drift, malformed data, or release failure must remain unavailable, never proved absence. Artifact labels do not claim complete lineage or current-serving authority.

The diagnostic does not run Docker commands, recover, clean up, change a latch, construct mutable stores, modify protected files, or replace the original failure. It does not log raw identifiers, paths, addresses, digests, configuration, or error text. The published candidate remains unchanged.

## Baseline

The already-started baseline used Go 1.26.7 on Windows with readonly module resolution. Both selected parents and all four subcases passed, with no skips or failures and unchanged Go/module inputs:

- `TestLiveGatewayRebindRuntimeDiagnosticRunnerCapturesBoundedResult`
- `TestLiveGatewayRebindRuntimeFailureDiagnosticUsesBoundedSnapshots`

The retained wrapper completed in 72.938 seconds. Its artifact prefix is `temp/m3-post-handover-diagnostics-20261011/baseline-runtime-diagnostics`; the JSON log SHA256 is `59f8db44868d4ca99ef49544138f78f2170f27012448dc1cc764f2940ccfc603`. An earlier missing Python executable prevented launch before any Go command; that attempt is retained separately.

## Final verification

The final Windows command used Go 1.26.7, `GOTOOLCHAIN=local`, and `GOFLAGS=-mod=readonly -buildvcs=false -p=1`:

```powershell
go test ./internal/generatedingress -run '^(TestLiveGatewayRebindRuntimeDiagnosticRunnerCapturesBoundedResult|TestLiveGatewayRebindRuntimeFailureDiagnosticUsesBoundedSnapshots|TestGatewayRebindRuntimeFailureStateDiagnosticClassifiesStableReads|TestGatewayRebindRuntimeFailureStateDiagnosticInspectionAllowsFailStopWithoutEffects|TestLiveGatewayRebindRuntimeCoordinatorDiagnosticKeepsFirstOutcome|TestGatewayRebindRuntimeFailureStateDiagnosticFreezesBeforeBoundedRead|TestGatewayRebindRuntimeFailureDiagnosticContextUsesFixtureContext)$' -count=1 -json -timeout=5m
```

All seven parents and thirteen subcases ran and passed exactly once, with no failures or skips. The package completed in 43.615 seconds; wrapper wall time was 55.265 seconds. All 797 Go/module inputs matched before and after. The independent assessment also compared the complete observed run/pass sets and rehashed those inputs against the final checkout.

The five new parents cover:

- Stable predecessor/successor classification, each recognized active SQL phase, foreign operation refusal, unknown labels, read errors, and independent SQL/protected drift.
- Actual read-only inspection with fail-stop set, preserving both fail-stop and commit-barrier latch values, SQL recovery snapshot, protected file fingerprints, and physical call counts.
- First coordinator outcome retention, including an unknown first failure.
- Original diagnostic capture before reads, a shared deadline, an earlier parent deadline, and cancellation without reads.
- The logger's use of the fixture journey context, including unavailable context.

Artifacts are under `temp/m3-post-handover-diagnostics-20261011/`:

| Receipt | Result |
| --- | --- |
| `final-runtime-state-diagnostic.result.json` | Go child exit 0; seven parents/thirteen subcases; source unchanged |
| `final-runtime-state-diagnostic.jsonl` | SHA256 `95BBFDBED7E35BF5DD68964B09796E8DFAEEFF6D709965762F6D41382570D350` |
| `orchestrator-assessment.json` | Exact observed sets, unique run/pass events, log hash, and 797 current source inputs verified |
| `source-security-review.json` | Final source/security GO for the two test files |

The original outer tool's terminal receipt was not retained. A premature process observation incorrectly classified that run as interrupted, despite the subsequently inspected successful child receipt. The incorrect `final-runtime-state-diagnostic.interrupted-launch.json` is preserved, with a separate correction. One redundant repeat had already started; it was collected through session 60454 with outer exit 0, seven parents/thirteen subcases passing, package 39.677 seconds and wall 42.141 seconds. Its unchanged-source log SHA256 is `77B3092117E7E6B212B248DD6D3EC76D8C24C5C2C9C75E55340013A4A5F455E1`. No further behavioral repetition was performed.

An earlier compile-only attempt failed on a misspelled test enum name; its diagnostic is retained as `compile-state-diagnostic.output.txt`, SHA256 `D32553293E169F2456847D40A238ACA147B66E9770B58A1C3E2C23EB08668997`. The corrected compile-only attempt exited 0. Those compile receipts pin the two changed test files and module files; the final behavioral receipts contain the full source manifest.

Scoped `go vet ./internal/generatedingress`, `gofmt -l` on both changed test files, and `git diff --check` exited 0; formatting produced no output. The full Go/module manifest remained unchanged. The static receipt is `final-runtime-state-diagnostic-static.result.json` in the same artifact directory. Production code is unchanged, so no additional production build or broad package suite was run.

`ci-parent-selection.json` evaluates the checked-in shared selector against all seven executed parents. Each maps to exactly one of its eighteen partitions: the four new `TestGatewayRebindRuntime...` parents map to `rebind-history`; the new coordinator recorder and two adjacent `TestLiveGatewayRebindRuntime...` parents map to `ingress-remainder`. These are ordinary tests and do not acquire an opt-in skip from their names. No CI files changed.

The documentation commands are `pnpm --dir docs check:workflow`, `pnpm --dir docs build`, and `pnpm --dir docs check:accessibility`. Their actual result is retained at `temp/m3-post-handover-diagnostics-docs-check-final-20261011/result.json`; this page does not preclaim the result of its own pending check.

## Acceptance limits

No local Docker daemon is available. Local tests cannot identify the actual hosted differing predicate or prove the typed Docker journey and independent cleanup pass. No new actual WSL or Docker execution is claimed for this test-only unit. The next hosted run remains required. Full controller crash recovery and physical second-device LAN acceptance remain separate incomplete M3 gates. Production code, authority rules, schemas, factories, timeouts, and CI selection are unchanged.
