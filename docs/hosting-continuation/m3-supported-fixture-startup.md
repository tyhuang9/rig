# M3 supported-controller fixture startup investigation

Status: diagnostic and cleanup repair candidate; M3 remains incomplete.
This branch starts from PR #160 at
`950944edc9e028ce407060e4e726521876ae1c14`. Its initial revision changes test
infrastructure. The follow-up adds closed diagnostic metadata to production
startup inspection while preserving its admission decisions.

## Observed hosted failure

The [supported-controller matrix job](https://github.com/tyhuang9/rig/actions/runs/38675225801/job/116253560413)
completed with failure on 2026-10-15 UTC. All nine prerequisite controls passed.
Each of the 33 boundary cases and 30 scenario cases then failed before the
initial full daemon reported readiness. The child exited with status 1, and its
stderr was represented only by a SHA-256 digest. The matrix package ran for
8,760.376 seconds. These results do not establish recovery acceptance or identify
the underlying startup error.

Each case also reported uncertain retained legacy v1 container, network and
configuration-volume ownership, followed by a duplicate builder-removal error.
The independent final cleanup step succeeded. That final census does not erase
the earlier per-case failures.

Raw log SHA-256:
`a99ade8f67c3f0a38c5ebe4212e5c4590ca573a7c518ffee25267d0a708bbec5`.
The immutable log, job/run metadata and parsed failures are retained under
`temp/m3-lan-external-db-20261015/supported-matrix-failure/` in the workspace
wrapper. PR #160 records the failed result alongside its separately scoped
successful jobs.

### First-boundary diagnostic at `12ccd60`

The [first-boundary diagnostic job](https://github.com/tyhuang9/rig/actions/runs/38702499754/job/116313833550)
completed with failure on 2026-10-15 at 08:36:58 UTC. Its prerequisite controls
passed. The single live `boundary/a0-sql-claim` child failed before initial daemon
assembly with `startup=gateway_inspection`, identifying the startup inspection
stage without exposing error details. The child ran for 109.20 seconds and the
package for 109.215 seconds. This code alone does not establish the underlying
cause.

The earlier per-case cleanup errors were absent in this observed case, and the
independent cleanup step passed. Post Set up Go was skipped following failure.
The dependent full matrix was skipped; neither the first boundary nor M3 is
accepted. PR #161 separately records eleven successful scoped job observations.

Raw log SHA-256:
`e4fbf77d4edea3d0dc7554b74e28be6169a7d307ce5e433879724e84b5ae122d`.
The immutable evidence is retained under
`temp/m3-lan-external-db-20261015/pr161-terminal-evidence/116313833550/` in the
workspace wrapper. Production startup validation remains required.

## Repair and verification design

- Retain the stderr digest and optionally add one constant startup-stage code.
  Only exact known structured-log messages and their explicitly enumerated
  startup checkpoints may select a code. Raw error attributes, paths, tokens
  and arbitrary text remain hidden. Parsing has byte
  and record limits; a primary startup failure takes precedence over subsequent
  emergency cleanup errors.
- Capture the legacy predecessor's identity before the fixture upgrades it.
  Apply ownership checks at cleanup, retaining uncertain resources and reporting
  errors. The existing two-app cleanup owns the retained legacy resources and
  builder; the later-generation cleanup no longer removes the builder twice.
- Add a separate first-boundary Docker job using the same supported daemon,
  environment flags, host prerequisites and independent cleanup census. It must
  discover and pass exactly the matrix parent and `boundary/a0-sql-claim` child,
  without failures or skips.
- Require this diagnostic job before the full matrix. The original 33-boundary,
  30-scenario execution, deadlines, background-isolation checks and final
  cleanup gate remain required. The diagnostic subset is not full acceptance.

The first diagnostic run identified the broad inspection stage but did not
justify a behavioral repair. The follow-up distinguishes fixed startup
checkpoint categories, including current selection, protected history, topology,
host-route and LAN-publication proofs. Private ingress metadata preserves the
public error contract. A private controller wrapper retains the original cause
for error inspection; this main log gate emits a fixed checkpoint instead of
the raw cause. Unknown metadata falls back to the broad diagnostic.

Validation order, cancellation, lock release and admission decisions remain
required. Recursive recovery proofs retain category-level diagnostics, so a
checkpoint is not guaranteed to identify an exact underlying cause. Runtime
authorization, immutable history and external application-owned database
behavior are unchanged.

## Initial revision verification record (`12ccd60`)

The unchanged baseline passed all nine local Windows control tests, with no
failures or skips. The candidate passed the same nine parents plus
`TestLiveHostdRebindClosedDiagnosticControls` and
`TestLiveHostdRebindLegacyV1OwnershipControls` on Windows and Linux: **11 parents
and 25 subcases per platform**, with no failures or skips. Both binaries also
discovered the live matrix parent exactly once without executing it locally.

Go 1.26.7 used `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'` and `CGO_ENABLED=0`. Linux binaries
ran under WSL. The control selector is the explicit list in both supported-hostd
workflow jobs. Local execution used `go test -tags live_docker -c`, exact test
discovery, and the compiled binary with `-test.count=1 -test.timeout=10m` through
`go tool test2json`; CI uses the equivalent `go test` selection with a two-minute
deadline. Source and binary hashes were checked before and after execution.

| Local check | Result |
| --- | --- |
| Windows and Linux production hostd builds | Passed |
| Windows and Linux `go vet -tags live_docker ./cmd/hostd` | Passed |
| Actual diagnostic jq filter | All 13 synthetic positive/negative cases passed |
| Workflow shell syntax and preservation comparison | Passed; all existing jobs and full matrix steps unchanged except the explicit control-list extension and new prerequisite |
| `gofmt -l` and `git diff --check` | Passed |
| Frozen documentation install, `check:workflow`, `build`, `check:accessibility` | Passed; the existing large-chunk build warning remains |

Receipts and exact commands are retained under
`temp/m3-lan-external-db-20261015/supported-fixture-verification/`.
The Windows control log SHA-256 is
`41e889c65f08486813e90156811eea9adddc0f30f817ed41b553a7bd4e0940bc`;
the Linux log SHA-256 is
`6dce47ba468a72be7f51434f080748b0212d9d327cf9f5bae68b4b0a99fc8817`.

Independent security and workflow reviews found no blocking issue. Code review
identified missing direct coverage of the final removal guard; the controls now
check its exact returned identities and denial behavior, including zero capture,
inspection errors, label disagreement and replacement IDs. The final Windows and
Linux reruns above passed, and code review found no remaining required change.
These are static independent reviews, not a CodeRabbit review or Docker result.

The diagnostics retain the existing complete in-memory stderr buffer for its
digest; the new classifier bounds only the prefix it scans. Docker volumes use
their canonical names as identity, while containers and networks are removed by
captured immutable IDs. This fixture requires a disposable daemon with no
pre-existing managed resources.

The local workstation has no Docker engine or race toolchain. Compilation and
pure controls do not establish real container behavior. The repository-wide
`make test` and browser suites were not repeated for the initial test-only change;
hosted CI provides the broader platform and race gates. The full supported
matrix and physical second-device LAN qualification remain outstanding.

## Checkpoint refinement verification

The refinement passed **118 parent tests and 416 subcases per platform** on
Windows and Linux, with exact discovery and no failures or skips:

| Package | Parent tests | Subcases | Windows seconds | Linux seconds |
| --- | ---: | ---: | ---: | ---: |
| `cmd/hostd` | 51 | 248 | 6.000 | 7.391 |
| `internal/generatedingress` | 67 | 168 | 166.094 | 21.219 |

The selected tests cover startup and rebind authority, retained history,
optional current selection, LAN recovery queues, transfer ordering, unlock
failures, cancellation, checkpoint privacy and parser boundaries. The workflow
now requires 17 fast controller controls before either live supported-hostd job.
Its actual jq filter passed all 13 synthetic cases; shell syntax and comparison
against the original workflow confirm the remaining matrix behavior is unchanged.

Both platform test binaries were compiled using
`go test -tags live_docker -c -o <temporary-binary> <package>`, then run with
explicit test discovery and `-test.v=test2json -test.count=1 -test.timeout=10m`
under the same Go environment described above. Linux binaries ran under WSL.
All 954 recorded source files matched before and after the final runs and match
the reviewed candidate. Windows and Linux `go build ./cmd/hostd` and
`go vet -tags live_docker ./cmd/hostd ./internal/generatedingress` also passed.

The first native ingress run failed because the new unlock test did not restore
its injected global function after its LAN subcase. Both subcases now register
cleanup, and the full selected suite passed on both platforms afterward. The
first run also detected a later controller-test edit through its source-stability
check; it is retained as failed evidence. Final controller reruns include that
extra malformed-field ordering regression. No failed result was treated as a pass.

Independent review also caught and resolved a cancellation-code change and a
malformed-checkpoint parsing issue. The final implementation retains the earlier
error classification and falls back to the broad diagnostic for malformed field
types. Static code and security reviews found no remaining blocking issue.

Exact command rosters, source and binary hashes, raw test logs, the failed first
run, and final results are retained in the workspace wrapper under
`temp/m3-lan-external-db-20261015/inspection-refinement-verification/`.

| Final raw test log | SHA-256 |
| --- | --- |
| Windows controller | `bd04224ee64c2ac1502d81eca30debe0c40e2fbfa08dfa7475720f2cb4539eed` |
| Linux controller | `55a1f4467f56f652500ab2a055ca94484f47fd360a17ba0aff753d82272e0a8c` |
| Windows ingress | `51c9ca05d015ee62c8db6d53c1bd4b2f31bd562489244783d0aa96094e19c62c` |
| Linux ingress | `a8e3fb8a6f80f398013e87d71865baf163cf97425949cc722848e7f08eeddf5a` |

The repository-wide `make test`, browser suite and race checks were not repeated
locally for this focused diagnostic change; hosted CI supplies the broader gates.
No local Docker or controller activation was performed. The next hosted
first-boundary result, full supported matrix and physical LAN qualification
remain required before M3 acceptance.

## Hosted snapshot failure and offline reproduction

At source `d5cf6f0`, [the hosted first-boundary job](https://github.com/tyhuang9/rig/actions/runs/38713250279/job/116349613973)
failed before the first child assembled the daemon. Its closed diagnostic was
`startup_snapshot`: the first combined SQLite read failed before protected-state
or Docker inspection. The 17 prerequisite controls and 121 subcases passed, as
did independent cleanup. The log SHA-256 is
`8c23259ab97949890984957f4ee985316671e94220218a130f3ba9be21c1143f`.
This is a failed boundary, not supported-matrix or M3 acceptance.

The offline regression
`TestHostingGatewayStartupSnapshotAcceptsSupportedFixtureCombinedStateAfterReopen`
constructs one committed native upgrade, two committed grants, two active runtime
heads and four components through public state transitions, using the established
SQL seeding pattern only for prerequisite application/release/deployment records.
It requires exact identities and complete snapshot equality before and after
closing/reopening the database. It checks unchanged claim/event/audit counts and
no writes during the reopened snapshot read. It does not execute controller
handlers, containers or protected-state inspection.

On unchanged production source, the regression and six related snapshot tests
passed on Windows and Linux: **7 parent tests and 17 subcases per platform**, no
failures or skips. Exact discovery and all 955 source hashes were verified. The
initial run exposed an incorrect test expectation using the reserved allocation
returned before grant commit; the fixture now reads the current active allocation
through the public repository after commit. The failed run remains recorded.

These successful offline reads do not reproduce or repair the hosted failure.
They narrow the missing evidence to live record details or the hosted environment;
no validator has been relaxed. Receipts and executable commands are in
`temp/m3-lan-external-db-20261015/snapshot-reproduction-verification/` in the
workspace wrapper. Final raw test log hashes:

- Windows: `2c42d5471e76d937e218729b21990a8f332113fb8c99b101f75d9a2230c2c9a3`.
- Linux: `dc84d1bff31be000817ace3d93ec032b57a6680375ba579221bdd4df7f88194b`.

## SQL stage and cause diagnostics

The next refinement observes the actual child's failing snapshot without changing
fixture setup. The repository returns a private wrapper at its nine existing
error boundaries: transaction begin, upgrades, grants, disables, rebind state,
runtime heads, runtime components, active approval authority, and commit.
Original error text, JSON serialization, unwrap identity, result-zeroing, read
ordering and transaction behavior are preserved. The dependency guard and all
validators are unchanged.

`HostingGatewayStartupDiagnostic` accepts only that direct private wrapper. It
returns a closed stage and cause class; it does not inspect error text or invoke
custom `Is`, `As` or `Unwrap` methods. Cause classification uses direct sentinels
and the concrete SQLite error type. The categories distinguish invalid stored
state, cancellation/deadline, SQLite busy/locked/query/corruption/I/O/other errors,
and unknown. Wrapped or unrecognized causes deliberately remain unknown.

The existing `gateway startup inspection failed` log emits only finite
`startup_checkpoint` and `startup_cause` fields. The live parser includes a cause
only for that exact message and a known snapshot checkpoint. Invalid field types
or values retain the stage diagnostic. Existing hashing, byte/record limits and
first-primary precedence remain intact. No raw error attributes are added.

The changed source passed **65 parent tests and 342 subcases per platform**:

| Package | Parents | Subcases | Windows seconds | Linux seconds |
| --- | ---: | ---: | ---: | ---: |
| `internal/appaccess` | 11 | 58 | 2.661 | 2.667 |
| `cmd/hostd` | 54 | 284 | 5.689 | 7.202 |

Both platforms used explicit discovery and uncached test binaries compiled with
`go test -tags live_docker -c`, then `go tool test2json` and
`-test.v=test2json -test.count=1 -test.timeout=10m`. There were no failures or
skips. All 957 source hashes matched before and after execution. Windows and
Linux `go build ./cmd/hostd` and
`go vet -tags live_docker ./cmd/hostd ./internal/appaccess` passed.

The workflow now requires 20 controller controls before each supported-hostd
live job. Its 13 synthetic filter cases and shell checks passed; comparison with
the earlier workflow confirms the full recovery matrix, dependencies, timeouts
and cleanup remain intact. Workflow SHA-256:
`9bda4866e72465382f186243fd087c53ac462899fa9694ee0f352d796eb4b93b`.
Independent static code and security reviews found no blocking issue.

Receipts, exact rosters and commands are under
`temp/m3-lan-external-db-20261015/snapshot-diagnostic-verification/` in the
workspace wrapper. Raw log hashes:

- Windows appaccess: `ba2674924e7f1d788c9e3c37cdaefb3f552e2874a8ba1867a3bc266bfcef1533`.
- Linux appaccess: `4e6d64c383ac58655de74bacc66e984a29576cfbd83c414de9822cdcb7011899`.
- Windows controller: `549bb72402d8b6db6583c46bea33f30b0b6328253f85e08c9da59b3b7b325e48`.
- Linux controller: `7aa9b486a8de9a736d3e1b5b8a14ba5b37aadd9c3be7118842a7ca6e6c683340`.

This is diagnostic coverage, not a repair or Docker acceptance. Local Docker,
local race execution and physical second-device LAN checks remain unavailable.
The repository-wide `make test` and browser suite were not repeated locally;
hosted CI supplies those broader checks for the published candidate. The new
hosted result must identify the failed stage/cause before selecting a behavioral
repair. M3 remains incomplete.

## Rollback

Revert the relevant diagnostic commit through the PR process if needed. No
production state or data format changes are involved. Reverting the initial
revision restores digest-only diagnostics and its earlier cleanup behavior;
reverting the refinement removes checkpoint detail. Neither action makes the
failed matrix an accepted result. No deployment or controller activation is
part of this change.
