# M3 supported-controller fixture startup investigation

Status: diagnostic and cleanup repair candidate; M3 remains incomplete.
This branch starts from PR #160 at
`950944edc9e028ce407060e4e726521876ae1c14`. It changes test infrastructure only.

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

## Repair and verification design

- Retain the stderr digest and optionally add one constant startup-stage code.
  Only exact known structured-log message literals may select a code. Error
  attributes, paths, tokens and arbitrary text remain hidden. Parsing has byte
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

There is no speculative production startup change. The first diagnostic run
must provide evidence for any subsequent repair. Runtime authorization,
immutable history and external application-owned database behavior are unchanged.

## Verification record

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
`make test` and browser suites were not repeated for this test-only change;
hosted CI provides the broader platform and race gates. The full supported
matrix and physical second-device LAN qualification remain outstanding.

## Rollback

Revert this test-infrastructure commit through the PR process if needed. It
does not change production state or data formats. Reverting restores the older
digest-only diagnostics and cleanup behavior; it does not make the failed
matrix an accepted result. No deployment or controller activation is part of
this change.
