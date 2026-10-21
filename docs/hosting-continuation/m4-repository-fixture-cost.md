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

## Follow-up: generated-image authority fixture

PR #196 runtime race job `118971706219` reached the unchanged 32-minute
`generatedimage` package deadline. Its 62 completed top-level tests consumed
1876.82 seconds; the active recipe parent was only 43 seconds old and its Vite
subtest 21 seconds old. The sampled runnable stack was in fresh database migration
through `newGeneratedImageTestAuthorityWithDependencies`. No earlier assertion
failure or race warning was observed. The stack identifies avoidable setup work,
without attributing every test's elapsed time to migration.

The follow-up adds one fresh constructor to the seven above: only its `db == nil`
branch now calls the unchanged `databasefixture.Open`. Every caller-supplied
database, repository and binding remains intact. In particular, the commit-
acknowledgement fixture retains its custom `sql.OpenDB` connector and explicit
`database.Migrate` call. Production, assertions, schema, CI selection and deadlines
remain unchanged.

The exact PR #204 baseline `f4995fce991303522d17d42ce164d858b848f6a9` and this
one-file candidate each passed the same 17 parent/subtest cases three times
(51 PASS events) as native Linux binaries, without race detection or skips.

| Representative parent | Baseline median (s) | Candidate median (s) |
| --- | ---: | ---: |
| Ambiguous missing builder inspections | 5.61 | 0.26 |
| Standalone recipe staging | 2.00 | 0.30 |
| Committed-ready result after lost acknowledgement (custom DB control) | 0.47 | 0.54 |

The first candidate builder sample was 0.75 seconds; later samples were 0.26 and
0.25 seconds. Cold template migration remains visible. The custom database control
continues to pay its real migration cost. No timing threshold was added, and these
measurements do not establish hosted performance.

Follow-up evidence is under
`m4-runtime-test-fixtures-verification/image-followup-20261021/` within the
local evidence directory above. Baseline receipt SHA256:
`9596d760da14708ba34e9437ed421be68e7e6c6da428409eee182f06ffafb431`;
candidate receipt:
`1e57028d8e964bca64dcd9bf4152aa783e73e8228596c3c76233d244d1d1cfd7`.
The candidate's 967-file source inventory is
`80bbcfef701eec5ea822fcaf66424456f1ce9c4bce89ac9b4205d5deb4de3952`.
The full package was discovered and executed once per platform with
`-test.run=^Test -test.count=1 -test.timeout=32m`:

| Platform | Discovered/run parents | RUN events | PASS | FAIL | SKIP |
| --- | ---: | ---: | ---: | ---: | ---: |
| Windows amd64 | 82/82 | 238 | 227 | 0 | 11 |
| Native WSL Linux amd64 | 82/82 | 218 | 211 | 2 | 5 |

Each RUN event has one terminal event. Windows had four conditional live-Docker
skips and seven symlink-capability skips; all eleven remain unverified.
The full Linux run **failed** because WSL has no Node executable:
`TestStaticBuildOutputCheckExecutesAgainstPresentMissingAndUnsafeDirectories`
and `TestNextCacheCheckRejectsMissingAndSymlinkedDirectories` stopped at their
existing Node prerequisites before running subcases. Linux also skipped the
Node-dependent static-server test and four conditional live-Docker cases.
Those missing subcases are not counted as passes.

The final static stage passed all 14 commands: generatedimage vet, production
build, test-binary compilation and production/test dependency inventories for
both targets; pinned read-only formatting, canonical generation and diff checks.
The helper remained absent from production CLI dependencies. All 967 Go/module
hashes remained unchanged. These static results do not replace the failed Linux
package result. No Node installation, test removal, assertion change or package
rerun was used to hide the failure.

| Follow-up artifact | SHA256 |
| --- | --- |
| Windows full run | `d19e11cfc2be433603e21357cb8bf7656cf1ee3bf2245924fdf40a06d9c28f4c` |
| Failed Linux completion receipt | `9eb203cdd9b68ffc15474017014915c76b3ef567e250e5387ab6620fb7e2110a` |
| Failed Linux full run | `7e05a70442473281252b403dbf0d91391f50412f13350763753dc7e5ae69fc26` |
| Successful static completion | `e6dacf79c1eac65dbce83b75f191ce986613e23e6793c623676e6e792ef42573` |
| Independent source and evidence review | `13c620b042495ebf0a0d82e014dc771f461b4d17d283ddd8a3c8abce754c565e` |

The original Windows audit and first static audit rejected incomplete expected
skip lists. Their failed receipts remain preserved; successor audits classify
the existing source-gated skips explicitly without rerunning package tests.
The original seven-fixture results above remain their historical accepted
boundary and were not rerun for this one-file follow-up. Hosted race/Docker and
Node-capable Linux execution remain required for complete follow-up acceptance.

Reverting the additional opener/import restores fresh migration for image
authority fixtures; no user data or runtime behavior changes.
