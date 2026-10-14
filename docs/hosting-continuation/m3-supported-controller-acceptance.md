# M3 supported controller candidate: acceptance evidence

Status: implementation candidate for draft CI; M3 acceptance is incomplete.
Core source `ed9bc5a966032263e3c27a61745708d58ea26ada` and full-hostd harness
`8206fd87dfd59196ee5bfe8e7097112bee655592` are integrated with published PR #153
`7d03eaf9db7c1742a1d5fb176255992f41900ca4`, preserving history. Local integration
`b24bf249e00645149eddd9471f989dfe9052e638` includes the reviewed A0 correction
`88ea2a8`. The full hostd harness compiles and its local controls pass; live
Docker execution remains pending behind prerequisites. No controller was installed
or restarted. These results do not authorize a GitHub merge or deployment.

## Scope and invariants

The candidate assembles the existing guarded rebind coordinators into normal
hostd startup and five canonical authenticated API operations. Startup has an
explicit refusing zero value, normal admission, and exact-operation recovery-only
dispatch. One Manager, repository, runner, protected root and ordered lease path
are retained. Immutable database commit or protected commit evidence cannot be
turned into rollback authority by a mutable `unresolved` phase. Recovery-only
routes remain restricted after reconciliation until a fresh process validates
normal startup.

Terminal success still requires exact retained command, SQL and protected
readback, selected current authority, final census, fence and release validation.
The Go-only observer reports immutable metadata; it has no response or authority
override and is not exposed by CLI, environment or HTTP. Distinct terminal install
and readback observations preserve mandatory validation between the two points.
Application databases remain external and application-owned. No managed database
or Neon provisioning was added.

## Core local checks

Pinned toolchain: Go 1.26.7. All checks used `GOTOOLCHAIN=local` and
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`. Windows and Linux binaries were
compiled separately with `CGO_ENABLED=0`; Linux binaries ran under WSL. Each
selected suite used explicit `-test.list` discovery, `-test.count=1`,
`-test.timeout=10m` and test2json capture. Every discovered parent ran; the runs
below have no failures or skips. They are focused suites, not a full repository
or race result.

| Suite | Windows parents / children | Linux parents / children |
| --- | ---: | ---: |
| Runtime constructor, immutable commit replay, P1 boundary, terminal install and releases | 9 / 0 | 9 / 0 |
| Canonical rebind API, authority, recovery restrictions and OpenAPI drift | 36 / 315 | 36 / 315 |
| Startup, pinned direction, dedicated recovery, emergency and worker admission | 22 / 105 | 22 / 105 |

Exact selectors:

```text
^(TestGatewayRebindSupportedConstructorInstallsConcreteDrivers|TestPrepareGatewayRebindLockedIntentBoundaryRetainsFence|TestGatewayRebindDurableBoundaryObserverReceivesImmutableMetadata|TestGatewayRebindTerminalRelease.*|TestGatewayRebindCommitReplayPreservesUnresolvedDatabaseCommitEvidence|TestGatewayRebindTypedCommitTerminalScansAndSelectsActualLineage)$
^(TestLANGatewayRebind.*|TestOpenAPIContractMatchesRegisteredRoutes|TestRecoveryOnlyControllerPinsOneOperationAndStaysRestrictedAfterRollback)$
^(TestGatewayRebindStartup.*|TestGatewayRebindRecoveryPin.*|TestGeneratedCompositionRecoversBeforeDeploymentJobAndWorker|TestGatewayStartupRebindFence.*|TestGatewayStartupEmergency.*|TestPrepareRuntimeWorker.*)$
```

Evidence is retained in `temp/m3-supported-controller-20261014/`. Each receipt
records input hashes before and after execution, exact commands, discovery,
RUN/PASS records, binary and log hashes. Internal suites exclude the concurrently
authored hostd test files from their source snapshot. Startup suites exclude only
the four live-tag harness files, which were neither compiled nor exercised by
those runs. The Linux API run preceded the later release-observer corrections;
its result is retained with that earlier snapshot. The Windows API, both runtime
and both startup results contain the final core production sources at `ed9bc5a`.

| Raw test log | SHA-256 |
| --- | --- |
| `ingress-reviewed-wsl.tests.log` | `021ec75db26cb66e924540785faa3b66f9e95fac047494a129abd0c3ebc00b4a` |
| `ingress-reviewed-windows.tests.log` | `6177be87939c448671eef3caa470f36d22ead843fb289742fbe0cb88837428db` |
| `controller-fast-wsl.tests.log` | `5c2864335364c4a45f785d9a54f0566942a292ae786979abd7dd9224c2a6be47` |
| `controller-reviewed-windows.tests.log` | `8f900c3893c9b44e1e731a8e8fcd8e22f211c5f93254b8c4301ad12070499de4` |
| `startup-fast-wsl.tests.log` | `6a9acd352af993b1bbb8da97ab118ca416146c0074b5696d365a05a463af9bd5` |
| `startup-fast-windows.tests.log` | `df9afd44b1d54119522b7a6a45348eef5058a9f89f50a29d8eab35d5281ea83f` |

The four CLI targets (`hostd`, `hostctl`, `rig-relay`, `rig-relay-probe`) build
on both platforms. Formatting, `go run ./cmd/openapi-gen -check` and scoped
`go vet ./cmd/hostd ./internal/controller ./internal/generatedingress` pass.
The generator regression passes nine cases: identifier initialisms preserve
`Identity`/`Idempotency` and still normalize identifier boundaries. The schema
permits generation zero for a native gateway-upgrade predecessor.

## Review repairs and executable failure evidence

Independent code and security reviews found a shared release-event clear that
ran after unlocking. A deterministic regression failed on the original code,
then passed after consuming the event while locked and retaining a local copy
through releases. The successor's event survives the previous operation's
return. Four release controls passed; the broader nine-parent runtime suites
above also pass on both platforms. The RED and GREEN logs remain under the
evidence directory; their SHA-256 values are respectively
`d113f797a5f11e3f0648e0920cce922ff76ff984edfafd64a914eaae166fb7e2`
and `782a25ecd0ec10a9892580a83bc2636432e419b68da64c115bfee39b8de2fe41`.

Review also corrected a startup test that incorrectly rejected protected commit
evidence alone and propagated the assembly readiness callback through existing
upgrade/LAN recovery. Final bounded code and security reviews report no remaining
core findings. CodeRabbit CLI was unavailable; these are independent manual
reviews with separately recorded executable evidence.

## Full hostd harness local verification

The Linux `live_docker` build compiles the complete crash matrix and its child
process. A focused run passes **28 parents / 117 children** without skips;
the standard Windows build passes **27 parents / 117 children**. The difference
is the Linux tagged copy-direction classifier control. These suites cover the
core startup selectors above plus the closed manifest, supported assembly,
real hostd contender admission and current-census mapping. They do not execute
the 62 live crash/recovery cases.

The fresh Linux binary SHA-256 is
`40711178b85d15615125ca47603d48d1e5213f0200314f3cbe7dd4604e73109a`;
its raw test log SHA-256 is
`c50306dedace5006d0ac289285751114cefb68eb67e85979c1bcfc548d188ce5`.
The Windows binary SHA-256 is
`9b3f37a2ae02c54ebafdb6ebbd1dca04b3608e9e3fbc93a012e8a3c1dd86643b`;
its raw test log SHA-256 is
`ba283f8cd460eb42e63fa7d9c724cba397a27b605681fc2c9ac3c8e61aca2a00`.
The `full-hostd-reviewed-*` receipts pin all Go/SQL/module inputs before and
after compilation, discovery and execution. An earlier assembly control failed
because it still named the replaced deployment fixture; that failure is retained
as `full-hostd-compile-wsl`, followed by the corrected green runs.

QA review corrected lost-ack copy matching, cancellation timing, recovery
direction assertions, exact commit replay, setup cleanup, atomic markers,
approval-to-reconcile revocation and the local-source pending-route scenario.
Copy inspection is excluded from the write ledger and has a tagged regression.
The CI preflight executes all three assembly/manifest/classifier controls before
creating resources. Both embedded jq filters passed real jq 1.8.1 positive,
missing, duplicate, skipped and failed-event controls; all four new shell blocks
passed Bash syntax checks using exact LF bytes. Matrix events in that selector
check are synthetic and prove only the acceptance filter. The workflow receipt
is `workflow-controls.result.json`.

Independent QA reports no remaining blocking static finding and allows draft CI
publication after these fresh checks. The ordinary queued-job isolation sentinel
is a live scenario; auto-deploy/relay omission is structural startup evidence,
not a separately seeded live acceptance result.

## Integrated-source review and verification

The combined Windows and Linux runs at `553c349` both exposed the same real
repository inspection defect: an absent protected projection omitted the required
explicit `none` disposition. Strict validation correctly rejected that empty
value. Correction `88ea2a8` fixes the producer, retains strict validation and
extends the real SQL/protected-state test to require `evidence_decides`, a held
fence, no advertised action and rejection of a mutated empty disposition. Its
existing assertions still require no protected-file changes or Docker commands.
The original failing binaries, raw logs and receipts are preserved as
`combined-ingress-*`; they are not passing acceptance evidence.

Focused verification passed with no skips or failures:

| Receipt / raw log prefix | Parents / children | Test log SHA-256 |
| --- | ---: | --- |
| `a0-projection-green-wsl` | 14 / 27 | `06d3f85abbc86181f9dff12a83af3c9ee7986dbca1cff3112d203e1a5e37874e` |
| `a0-projection-focused-windows` | 3 / 10 | `3caddc2372efe09a7455abe6c569ba773fcf3eb89034a58e9ce61e20f014e069` |
| `final-api-wsl` | 37 / 322 | `a8adc73044fb48b383ff3e8f99c7f2cf7d1286d5d232fb33ef392e7caea48437` |
| `final-diagnostic-windows` | 7 / 23 | `badbdc004f6bf18a9ced4db0a0db97a88306ec588df8ca115a254638c0093d30` |

The first two rows contain the A0 correction before the diagnostic-only parent
integration. The last two rows run against `b24bf249e00645149eddd9471f989dfe9052e638`
and verify the integrated authenticated API, schema, finite diagnostics and
A0 behavior. Each receipt records its exact selector, discovery and source/binary
pins. The fresh `final-cli.result.json` builds all four CLI targets and runs scoped
vet on both Windows and Linux. Its production source hashes match this integrated
candidate; the subsequent parent integration changed only two test files and docs.
This supersedes the earlier production-build snapshot for compilation evidence.

A broader Windows selection of fourteen parents reached its unchanged ten-minute
timeout in the database-heavy audited-reconcile test. Eleven parents and fourteen
children had passed, but two discovered parents never started. The strict runner
rejected incomplete discovery accounting; the raw timeout log, original progress
and `a0-projection-green-windows.incomplete.json` are preserved. The log SHA-256 is
`094ac9c9d665aca8220aa7356d09bdcc84e4c604df5ba45ccd6afc52f0a2ae6b`.
This broad Windows run is incomplete, not green. The targeted Windows A0 check and
later seven-parent integration check above passed; Linux completed the broader
fourteen-parent selection. Full hosted Windows and race gates remain required.

Independent final review found the A0 source defect resolved with executable
coverage and cleared draft CI publication, while retaining all remaining gates.
The new crash matrix depends on every `rebind-cross-store-live` matrix member.

At published parent `7d03eaf`, the diagnostic hosted
[job 116124828686](https://github.com/tyhuang9/rig/actions/runs/38643459027/job/116124828686)
reported `problem-unavailable` after approved reconcile while retained state showed
no active SQL claim, a commit receipt, a current artifact and successor authority.
This narrows the failure to a path that can occur after committed state exists;
it does not yet identify the failed check or prove a successful HTTP journey.
The controller recovery prerequisite remains failed and under investigation.

## Remaining acceptance and rollback

The full hostd crash harness must pass its exact 33-boundary / 29-scenario Docker
matrix, with independent fixture cleanup, on the candidate source. Compiling it
and checking its manifest do not establish any live case. Full repository,
Linux race and hosted Windows gates are still required. Auto-deploy and relay
isolation must be distinguished from the ordinary queued-job sentinel and from
structural evidence that recovery-only startup returns before creating loops.

Physical qualification still requires a second device, all roster applications,
redeploy/restart, address drift, old-interface removal/return, stale-address
withdrawal and the application-owned external database create/read path. Local
or namespace probes do not replace this gate.

Before an operation exists, the candidate source can be reverted. After an
operation exists, disable new admission while retaining compatible recovery
code, immutable approvals/audit, SQL migrations, protected history and fences.
Never undo a committed transfer, delete guessed resources or clear a fence
manually to simulate rollback.
