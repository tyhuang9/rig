# M4 repository test fixture cost

Recorded 2026-10-21. Baseline: `97b72d2159c0b7ac8e8bf265f782326313fa7623`
([draft PR #193](https://github.com/tyhuang9/rig/pull/193)).

## Observed hosted failure

Repository race job `118948757093` reached its 80-minute job limit after four
packages reached cumulative 40-minute package deadlines. Three snapshots were
inside fresh database migration; the lifecycle snapshot was inside a domain query.
These snapshots do not establish a deadlock or a 40-minute individual test.
Hosted checkout `fb76daee7d34025541ed732bfcb25bdbf90032e0` and the PR head had the
same tree, `16c13d730af29f8bbc151d7f3e14d2346b84b8c7`.

This patch reduces repeated schema setup in tests. It does not claim a complete
root cause or successful hosted timeout remediation.

## Narrow change and invariants

Seven fresh fixture constructors now use the existing isolated
`internal/testsupport/databasefixture.Open` helper:

| Package | Constructor |
| --- | --- |
| applifecycle | `newLifecycleFixture`, `newOperationFixture` |
| composeruntime | `newBoundComposeJob`, `assertRepositoryParity` |
| deployments | `newRepositoryFixture` |
| generatedexecutor | `newGeneratedExecutorClaim`, `newCapacityResumeFixture` |

The helper copies an immutable empty migrated schema into a separate private
file per fixture, then opens that file through production `database.Open`.
It shares no live connection or application/job/runtime state. Its implementation
and tests are unchanged. Every fixture keeps its original private directory, seed
data, connection settings, transactions, assertions and cleanup.

All five other production opens in these files remain: two standalone concurrent
CAS handles, the second operation-acceptance handle, pending-start persistence
reopen and deployment-gate rollback reopen. Production code, migrations, CI
selection, race flags and time limits are unchanged. Both target-platform
production CLI dependency inventories exclude the test fixture helper.

## Measured comparison

Pinned Go 1.26.7; Linux amd64 test binaries executed through native WSL. Each of
seven representative parents ran three times in one process per package, using
`-test.count=3 -test.timeout=10m`. Baseline and candidate each had the same 54
parent/subtest PASS events with no failure or skip. Compilation was recorded
separately. These local measurements did not use race detection.

| Representative parent | Baseline median (s) | Candidate median (s) |
| --- | ---: | ---: |
| Desired lifecycle transitions and replay | 0.46 | 0.02 |
| Transaction-local positive fence validation | 0.50 | 0.04 |
| Compose GitHub/local read-only workspace | 1.91 | 0.07 |
| Compose scope fingerprint/repository parity | 0.46 | 0.02 |
| Pinned release runtime strategy | 1.39 | 0.04 |
| Candidate capacity retained on image refusal | 1.86 | 0.06 |
| Paused capacity resumes with one replacement | 0.53 | 0.05 |

The first candidate fixture in each package still pays for template migration:
the recorded first samples were 0.52, 0.58, 0.53 and 0.56 seconds. Later fixtures
reuse only the empty schema. Medians therefore describe this repeated-fixture
workload, not a guarantee about a cold single test, production speed or CI timing.
No timing threshold was added to tests.

## Full affected regression boundary

All tests in `internal/applifecycle`, `internal/composeruntime`,
`internal/deployments`, `internal/generatedexecutor` and
`internal/testsupport/databasefixture` were discovered and executed with
`-test.run=^Test -test.count=1 -test.timeout=40m`.

| Platform | Discovered parents | Parent PASS | Subtest PASS | Skips |
| --- | ---: | ---: | ---: | ---: |
| Windows amd64 | 166 | 164 | 372 | 2 |
| Native WSL Linux amd64 | 166 | 166 | 372 | 0 |

Windows lacked symlink privilege for `TestPolicyRejectsSymlinkAncestor` and
`TestOpenRejectsControlDatabaseSymlink`; both executed and passed on Linux.
No other test was skipped. Helper coverage includes production schema/data
equivalence, independent concurrent clones, production reopen and artifact refusal.

With pinned Go, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false` and `CGO_ENABLED=0`, these checks passed:

- `go vet` for all five packages on both target platforms.
- `go build -o <evidence-output> ./cmd/hostd` for Windows and Linux.
- `go list -deps ./cmd/...` for both platforms; fixture helper absent.
- `gofmt -l` on the seven changed files: empty output.
- `pwsh -NoProfile -File scripts/check-generation.ps1` and `git diff --check`.
- All 967 Go/module hashes unchanged throughout final verification.

## Evidence and remaining acceptance

Local evidence directory:
`temp/m3-lan-external-db-20261015/m4-runtime-test-fixtures-verification/`.

| Receipt | SHA256 |
| --- | --- |
| Baseline measurement | `e61c1e0cdee2a0a8aca87ba30660b3496f5b6e64854562dc16c57c01ef482981` |
| Candidate measurement | `080445d166ea3969e05991da1c772285728f1fd986353bba2762e34c05d45f8b` |
| Comparison and exact case parity | `37d3181975c218e9e8c6e9f821de2b98561bfedfbd8191974ffa29a03df6e935` |
| Full verification | `8f292cb1892e8a440dd1fe953ae1fd76f99c17e4fe6aedb6820071ff206088a2` |
| Final 967-file source manifest | `d1735d0c70079c46a76e1e8a54ea036adc09547e503856a72ddbdf7f2dded715` |
| Original hosted failure log | `81288ef163654f28d2db731796134ae3722ac459b53dc4fbd42db3bd12433644` |

Local race tooling was unavailable. The unchanged hosted repository-remainder
race gate remains required; local non-race completion cannot establish it.
No manual product interaction, live controller activation or deployment was
performed. This test-only change does not close M4 or the broader continuation.

Rollback consists of reverting the seven test opener/import changes. It restores
fresh schema migration per fixture, without changing user data or runtime behavior.
