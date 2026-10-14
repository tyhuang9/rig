# M3 supported controller candidate: acceptance evidence

Status: implementation candidate; M3 acceptance is incomplete. Core source is
`ed9bc5a966032263e3c27a61745708d58ea26ada`, based on the original PR #153 candidate
`76cf574dce603793ff2509e4a90ac6cc9de47bc5`. The full hostd harness compiles and its
local controls pass; live Docker execution remains pending. No controller was
installed or restarted, and no merge or deployment
is authorized by these results.

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
