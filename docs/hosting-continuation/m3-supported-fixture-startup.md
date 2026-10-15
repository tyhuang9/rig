# M3 supported-controller fixture startup investigation

Status: terminal-proof fixture correction candidate; M3 remains incomplete.
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

## Materialized release identity repair

The diagnostic at `6b43271d73eaf88ea9a86359c4bd7a158b18791f` narrowed the hosted
failure to `startup_snapshot_runtime_heads` with cause `invalid_stored_state`.
[Job 116379868073](https://github.com/tyhuang9/rig/actions/runs/38722389935/job/116379868073)
failed the initial `boundary/a0-sql-claim` child before daemon assembly (150.12s).
All 20 prerequisite parents and 157 subcases ran and passed once, and independent
cleanup passed. The checkout tree matched the published head. Raw log SHA-256:
`a6a3409b930aecbbfa3a4ea66e6c7b61cb8248ae7c6929d73c0ac3d865234f7d`.

Both local and GitHub snapshot materializers generate 16 random bytes encoded
as 32 lowercase hexadecimal characters. Generated runtime state already accepts
that format and canonical UUIDs. The startup head reader, rebind roster/head
digests, serving census and retained-head SQL insert trigger instead required
UUIDs. The offline fixture used a UUID, hiding the producer/consumer mismatch.
Changing its release IDs to the materializer format reproduced rejection against
unchanged production. The failed native reproduction is retained with log SHA-256
`f11eb0183c5e5d6b75eb363a1ba40b5045f4ef151a1cf018846e1311e202bbaa`.

The repair applies a release-specific canonical UUID-or-hex32 check at the five
Go validation sites and aligns the two rebind API fields with the existing
general release-ID contract. It preserves exact ID bytes, digest versions and
serialization, SQL relationships, authority checks and immutable history. Other
identifiers remain UUID-only. It adds no ID normalization, retry, timeout or
Docker behavior change.

The first broader verification exposed the same restriction in migration 035's
retained-head insert trigger: 140 appaccess parents passed on each OS and the
new hex32 claim-persistence case failed. Migration 035 remains unchanged. The
additive, mirrored migration `037_lan_gateway_rebind_release_identity.sql`
replaces only that insert trigger. All predicates outside release validation
remain identical, including prepared-claim authority, exact runtime parents,
ordinal ordering, slot/generation and timestamp checks. Release values must be
text with exact character and byte lengths; embedded NUL suffixes and BLOB
values are rejected. No existing row or digest is rewritten, and update/delete
retention triggers remain installed.

### Verification boundary for the repair

Local verification receipts are retained in wrapper
`temp/m3-lan-external-db-20261015/release-identity-verification/`. They distinguish
the failing producer-format reproduction, the intermediate SQL-trigger failure,
and subsequent migration-enabled results. Verification includes both valid
formats, malformed boundaries, crossed release relationships, legacy digest
compatibility, retained claim replay, migration upgrade/rollback and unchanged
history. The final source passed **214 parent tests and 360 subcases per
platform**, with explicit discovery, uncached execution and no failures or skips:

| Package | Parents | Subcases | Windows seconds | Linux seconds |
| --- | ---: | ---: | ---: | ---: |
| `internal/appaccess` (complete package) | 141 | 278 | 26.703 | 28.281 |
| `internal/database` (complete package) | 60 | 50 | 21.094 | 23.235 |
| `internal/generatedingress` (serving restore/census) | 11 | 30 | 309.953 | 42.157 |
| `internal/controller` (release contract/routes) | 2 | 2 | 0.735 | 0.907 |

The runners compile with `go test -tags live_docker -c`, discover each exact
roster and execute through `go tool test2json` with `-test.count=1` and
`-test.timeout=10m`. These selected tests do not require Docker. Recorded source
hashes match before and after each run. Non-database runs exclude only the new
database test file from their hash set; that file is not compiled by those
packages and is covered by the complete database runs.

Windows and Linux `go build ./cmd/hostd` and
`go vet -tags live_docker ./cmd/hostd ./internal/appaccess ./internal/generatedingress ./internal/database ./internal/controller`
passed. `pwsh -NoProfile -File scripts/check-generation.ps1` passed, including
the migration mirror, generated OpenAPI artifacts and registered-route checks.
The frozen offline frontend install, all 544 frontend tests and canonical
`scripts/check-embedded.ps1` production build/asset comparison passed. Docs
workflow, build and accessibility checks also passed. Independent static
security review found no blocking issue.

Final raw test log SHA-256 values:

- Windows appaccess: `50db821bc03a7982321d518b1b6b4d7a362fc81f46b634ef06e8d11eb70d765a`.
- Linux appaccess: `4e77c90478e06c92abba511b64118f0b96b41ecc4e672940dab11086d1f50d0e`.
- Windows database: `86e041220e8c5b15be9e5e56b3094229885aa2f69cd9f5ad747a7527b6ea52a2`.
- Linux database: `b3f644322762c6def075dd06b82f7569c8fe214666530488479348234527b453`.
- Windows ingress: `1739c65b804819173e104bb56c2fdc3bf26b84a8d9bd4bc75f6ce8c094dedc46`.
- Linux ingress: `5be7b41f63b78feeefd46501f55d513431c25d602cd8079e25c7cb88eee09092`.
- Windows contract: `7e611489886c79e44bf9b2f93772560c27b448a91984e617cb8d2b00a26152e5`.
- Linux contract: `81fdf8a415a73f161513e38589a4c10e88c9ca8ed7c34b9ed1f95b5ff9fa7714`.

Local Docker and race execution remain unavailable. Repository-wide `make test`,
the browser suite and manual screen-reader checks were not repeated locally;
the published candidate needs hosted platform and Docker verification.

The preceding hosted passes apply to `6b43271`, not the release-identity repair.
The repair still requires hosted initial-child acceptance followed by the full
33-boundary/30-scenario matrix. Physical second-device LAN qualification remains
separate. External databases remain application-owned and configured through
scoped runtime secrets; no managed database or Neon provisioning is added.

### Migration and rollback limits

The existing migration runner applies the trigger replacement and its ledger
entry in one transaction. This development work does not restart or migrate the
user's running controller. Migration 037 runs when a controller with this code
opens its database through the normal migration path.

A code revert does not remove an already-applied migration. Older readers reject
materialized hex32 IDs, so reverting can restore the startup failure. Any
predicate downgrade requires a separately reviewed additive migration. Never
rewrite release IDs or delete operation history to make an older reader pass.


## Unpersisted successor fixture correction

The release-identity candidate `5637b68fd7426517a29152bb2f5edfc81db75e21`
advanced through normal daemon readiness and the successor advisory read.
[First-boundary job116402057714](https://github.com/tyhuang9/rig/actions/runs/38729070015/job/116402057714)
then failed `configure successor profile: status=409 error=<nil>` (140.26s
child). Its actual checkout tree matched the candidate; all 20 prerequisite
parents and 157 subcases passed once, and independent cleanup passed. Raw log
SHA-256: `ef5b135c0dde3f7c1581e710c15423c5ae59748eafe4e0c0f96219fa989e893f`.

Static tracing found a deterministic fixture/protocol mismatch. The fixture
retains a committed upgrade and active allocations, which intentionally block
ordinary profile configuration. It nevertheless attempted that ordinary POST
before requesting a rebind proposal. Rebind admission requires a fresh,
unpersisted successor identity; an already stored successor is also rejected.
The HTTP status alone does not identify the exact public problem code.

The fixture now plans a successor from the advisory GET, retaining fresh
revision/operation identities and current revision plus one through proposal
review and commit. It does not mutate the ordinary profile endpoint. Production
authorization, profile pins, exact relationship checks and SQL history remain
unchanged. The new offline planning control is required before both hosted
supported-daemon jobs. The 33-boundary/30-scenario matrix, timeouts, Docker
behavior and cleanup remain unchanged.

The final fixture source passed **28 parent tests and 168 subcases per OS**,
with explicit discovery, uncached execution and no failures or skips:

| Scope | Parents | Subcases | Windows seconds | Linux seconds |
| --- | ---: | ---: | ---: | ---: |
| Required controller controls, including successor planning | 21 | 168 | 2.844 | 3.859 |
| Real SQL profile pins and unpersisted claim contracts | 4 | 0 | 1.031 | 1.344 |
| Authenticated profile read/conflict contracts | 2 | 0 | 1.547 | 1.797 |
| Rebind proposal/admission with real repository | 1 | 0 | 1.735 | 1.390 |

The runner again used `go test -tags live_docker -c`, exact test discovery and
`go tool test2json` with `-test.count=1 -test.timeout=10m` on Windows and WSL.
Every recorded Go/SQL/API source hash matched before and after execution. The
new HTTP regression exercises the actual fixture helper and request construction;
its simulated server does not establish runtime or Docker acceptance.

Windows/Linux CLI builds and live-tag vet passed, as did canonical generation,
final docs workflow/build/accessibility, formatting and whitespace checks. The
workflow check passed 13 synthetic cases against the actual first-boundary
filter, checked shell syntax, and confirmed other jobs, full matrix, deadlines
and independent cleanup were unchanged. Workflow SHA-256:
`f10ed280f1136d68a27664ba493f5234749debbe6af036ccfe7bbcc14244ea5e`.

Exact commands, source pins and logs are retained in wrapper
`temp/m3-lan-external-db-20261015/successor-fixture-verification/`. Final controller
control log SHA-256 values are
`9e7f1e564b1a540048f13e0dd240dc644f24176e30f86234e6df6e20339b5d3f`
(Windows) and
`847049cc6fac83151d3f33b52d90f3a6720767aa2423a921639bb3991294aa10`
(Linux). Independent static integration review found no blocking issue.
Local race and Docker checks remain unavailable; broad repository/browser and
frontend suites were not repeated for this fixture-only follow-up.

This is a test-harness correction, with no new production migration or runtime
activation. A reviewed revert restores the earlier incorrect fixture request
sequence. Hosted initial-boundary and full-matrix acceptance remain required;
physical second-device qualification is still separate.

## Terminal proof fixture contract correction

The successor-planning candidate `5ecefd7fc083781decf6d7dbad7375abd1734547`
passed all 21 prerequisite parents and 168 subcases once in
[first-boundary job 116412922117](https://github.com/tyhuang9/rig/actions/runs/38732307284/job/116412922117).
The actual checkout `242ad5b62cf0195715158df72e88b68655c2e698` had the same
tree as the candidate. The live `a0-sql-claim` child failed after 294.65 seconds:
its operation was historical, inactive and released, but terminal proof was
`unavailable`. Independent cleanup passed. Raw log SHA-256:
`d4126beee646f5293f5d2a1c0b8e78a82418038583a4d93d382ead5da6cfc7c3`.
The collapsed helper location does not identify which recovery/read phase
failed; the complete matrix remains unaccepted.

Static tracing found two fixture contract errors. An ordinary status GET is a
read-only projection of retained state and deliberately leaves physical proof
unavailable. The authenticated retry of the exact original commit request is
the existing normal-mode path for freshly verifying terminal and current
physical evidence. Recovery-only mode uses the existing audited reconcile
path. Also, `terminalProofDigest` identifies a purpose-bound physical proof;
it is not the protected terminal receipt digest. The latter is exposed as
`operation.terminalReceiptDigest` and retained in the terminal SQL command.

The correction must keep terminal proof mandatory, bind any retry to the exact
original request and retained operation/current identities, and compare the
protected receipt with the receipt digest in both the API and SQL. It must not
turn an unavailable GET into verified evidence without a successful fresh
proof response. Production behavior, recovery authorization, SQL history and
the 33-boundary/30-scenario requirement remain unchanged.


### Strict terminal fixture verification

The fixture now retains defensive copies of each original reviewed commit
request and rejects replacement under the same operation identity. Normal-mode
replay requires HTTP 200, no creation or recovery requirement, released fencing,
both verified physical proofs, and exact complete operation/current-authority
equality with the diagnostic read. Failure cannot fall back to GET evidence.
The protected receipt binds the API receipt digest and final SQL command's
operation, sequence, state, disposition and receipt digest. Audited reconcile
keeps its existing path and uses the corrected durable receipt assertion.

Two new offline controls are mandatory in both hosted prerequisite selectors
and discovery assertions. The initial combined Windows ingress invocation hit
its existing ten-minute deadline during the final reconciliation parent's
`fixed_abort` subcase after the other seven parents passed. Its failed raw log
is retained with SHA-256
`4c9c1dc2373a49d50a4894b7df3d1489235c534f43ec5ef5dd494aabb3a91749`.
The same eight parents were then rerun in three disjoint invocations, each
retaining `-test.count=1 -test.timeout=10m`. No source or CI deadline changed.

Final verification passed **37 parents and 247 subcases per OS**
with exact discovery, uncached execution, no skips, and matching source hashes
before and after execution. Windows ingress time is the sum of its three
successful partition durations:

| Scope | Parents | Subcases | Windows seconds | Linux seconds |
| --- | ---: | ---: | ---: | ---: |
| Required controller controls | 23 | 192 | 2.213 | 3.268 |
| Production replay/reconcile contracts | 8 | 24 | 986.141 | 125.527 |
| Authenticated controller contracts | 6 | 31 | 0.019 | 0.186 |


Commands again compile with `go test -tags live_docker -c`, explicitly discover
the recorded rosters, and execute through `go tool test2json` using
`-test.count=1 -test.timeout=10m`. Windows/Linux CLI builds and live-tag vet
passed, as did canonical generation and whitespace checks. Docs checks are
recorded with the publication evidence. The actual workflow filter passed
13 synthetic cases; syntax and structural comparison preserve the complete
33-boundary/30-scenario matrix, other jobs, deadlines and independent cleanup.
Workflow SHA-256:
`bff40dfe88c5876d73b8d1a8e4e65b3c812aece0f8a4e5f306144dc2f72b468b`.

Source pins, exact commands and raw logs are retained in wrapper
`temp/m3-lan-external-db-20261015/terminal-fixture-verification/`.

- `cmd-final-native`: `292fc912787e4119a3660d80388a41d48a0ac327ed19ee49b32872cd79315f57`.

- `cmd-final-wsl`: `fa4f6c3c7e53802098342133d02f3aa8852b5a550b77c0dc434a9ecd7b5ae1ac`.

- `ingress-core-partition-native`: `26e8019791bd87c34c8a14ba58a37a76c48df0a7edacae4c3d7cfbd5160757e7`.

- `ingress-replay-partition-native`: `a8f9cc4147534adde1bdd765f7ca5f8cc37a637c091d8b0a0b35d804cead90e2`.

- `ingress-reconcile-partition-native`: `1a98be57e7ffc19bc7f32003b137253db732c7d66d955ec53776c2051ba7113b`.

- `ingress-final-wsl`: `0bb932d1189e1f16f1dec1a40be73c2579faae25147235fae0a1da4dc8749afe`.

- `controller-partition-native`: `89607ff8148c683027c1419196e4409992d2b5bf0bd425fa83a504560ea89aa6`.

- `controller-final-wsl`: `ed5c304027b4028f8b6d11a7249ae76ed51fa0a18b445e0dfebd5a632aa1e4e0`.


Independent static integration review found no remaining blocker after fixing
synchronized test observations and same-operation request-replacement coverage.
This is not CodeRabbit review. Local Docker/race execution remains unavailable;
broad repository, frontend and browser suites were not repeated for this
test-only correction. Real initial-boundary and full-matrix acceptance remain
required. The pending-route historical-current scenario stays strict and has
not been accepted; a later current revision must not acquire historical physical
proof by assumption. Physical second-device qualification is separate.

This follow-up changes only test helpers, controls, CI prerequisites and this
evidence document. A reviewed revert restores the earlier incorrect fixture
expectations. It does not change production state or migration history, and
the user's running controller has not been restarted or deployed.
