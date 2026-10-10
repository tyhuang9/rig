# PR 137 race fixture capacity repair

Status: local test-only implementation and bounded verification completed;
independent security and final source reviews passed. No new hosted race result or
aggregate capacity improvement is claimed.

Published base: `1b3eb244c2f29d30b7c8b77faff5bcca995f66d3`. Publication of this
follow-up still requires explicit approval. No merge, workflow cancellation or
production activation occurred. The original timeout evidence remains failed
acceptance evidence.

## Observed race failures

Ten original hosted jobs at `488046bcfc98000f70ea03322dbfe1084bec6e78` timed out
after 32 minutes. They cover six distinct ingress groups in two workflows.
The shared runner still uses `-race -count=1 -timeout=32m`; the complete group
selections and required aggregate checks remain unchanged.

The database implementation, embedded migrations, common predecessor fixture
and shared suite runner have no diff between that original source and the
published base above. The later fixture repairs do not change this setup path.

All saved job logs are under `C:/Users/huang/Documents/Projects/Rig/temp/` with
names `pr137-job-<job>-attempt1.log`. The read-only parsed inventory, per-log
hashes, actual suite identity, completed counts and timeout attribution are
saved in `m3-pr137-race-attribution-20261009.json`, SHA-256
`850BFC8FA5A35C53C14DD179C0B6665BF906D9F82C126EF35EA6604D2AD97649`.
Expected group membership comes from the retained compiled Linux inventory in
`m3-capacity-linux-coverage-20261008.json`, not a new full-suite run.

| Original job | Group / expected parents | Parent / subcase passes before timeout | Completed-parent seconds | Active leaf / parent seconds at timeout |
| --- | --- | ---: | ---: | ---: |
| `113462431300` | rebind-history / 137 | 49 / 118 | 1882.77 | 37.16 / 37.16 |
| `113462431422` | rebind-resources / 78 | 44 / 104 | 1818.85 | 8.39 / 101.13 |
| `113462431423` | current-runtime / 32 | 31 / 35 | 1918.15 | 1.87 / 1.87 |
| `113462431501` | rebind-typed-runtime / 22 | 20 / 39 | 1777.13 | 6.25 / 142.89 |
| `113462431507` | rebind-config / 55 | 47 / 143 | 1879.93 | 19.14 / 40.07 |
| `113462428908` | current-serving / 55 | 48 / 41 | 1733.72 | 59.71 / 186.25 |
| `113462428918` | current-runtime / 32 | 31 / 35 | 1894.05 | 25.95 / 25.95 |
| `113462429529` | rebind-resources / 78 | 39 / 87 | 1903.65 | 16.30 / 16.30 |
| `113462429548` | rebind-config / 55 | 47 / 144 | 1870.22 | 8.47 / 49.83 |
| `113462429567` | rebind-history / 137 | 67 / 172 | 1911.19 | 8.80 / 8.80 |

These are partial failing packages. Passed subcases beneath an unfinished parent
do not make that parent pass. The package timeout durations were approximately
1920 seconds; completed-parent time plus the active parent accounts for that
budget. In one current-runtime job the last parent had run for only 1.87 seconds.
This supports cumulative capacity exhaustion. A sampled stack and partial log
cannot establish that every individual operation was free of stalls.

### Published repair attempt remains failing

As of 2026-10-09 14:35 UTC, eight further race jobs at the published `1b3eb244`
source reached the same 32-minute package timeout. These jobs did not contain the local database
template change. Their logs are preserved; they are current-attempt failures,
not acceptance evidence for the candidate helper.

| Job | Group / active test at timeout | Log SHA-256 |
| --- | --- | --- |
| `113849127743` | current-runtime / `TestGatewayCurrentServingRestoreActionRejectsIncompleteOrAlteredTarget` | `9074B6AE08FE84E3BB3DD1A56809A527499633A89C07E557061BCEA629C3315F` |
| `113849127846` | current-serving / `TestGatewayCurrentTerminalEmergencyStopsDespiteCorruptCurrentBundle/missing` | `A64873261157ED5A097543CCB1CD0747F4E6C97C02F1D6FE0069887F4FC18A3D` |
| `113849128394` | rebind-history / `TestGatewayRebindEffectBoundaryCancellationAndLockFailuresEraseEvidence/gateway_lock_acquisition_fails_and_releases_lease` | `A2B0208685DF68C84C08901DD2875B646AAD266667BB5AFF9D55A4E7E366E2EB` |
| `113849127727` | rebind-typed-runtime / `TestGatewayRebindTypedStageRuntimeRejectsAuthorityDriftAfterFinalArchive` | `AE63758256DBC919F651181FFB917818A11B3AF2B330F16575D0518CA74B3E72` |
| `113849127804` | rebind-resources / `TestGatewayRebindStageNetworkReleaseErrorsPreserveExactBinding/gateway_OS_lock_release` | `F64FB4910459257A6328489C6590B464E687C38047047DA2D7EAD64EAA200CAB` |
| `113849128216` | rebind-history / `TestGatewayRebindEffectBoundaryHoldsDeploymentEffectsLeaseAgainstAnotherProcess` | `025D151AFFF0A56DEB94382D5F472789480481398A99297995E7A6BBBE542552` |
| `113849128025` | current-runtime / `TestGatewayCurrentServingRestoreComposesSQLWithConcreteExecutor/stable` | `2AC5397AA87FCD51B80AA518285412D7F6A3CBDD9149B521E18E61AAE8FA63D7` |
| `113849128144` | rebind-resources / `TestGatewayRebindStageNetworkReleaseErrorsPreserveExactBinding/gateway_OS_lock_release` | `33BDFEA2E88957F112C8059CC5ADE0BA9B0E0334EC8B5FD8F1EA357E591C9BE1` |

Each log contains `test timed out after 32m0s` and no `WARNING: DATA RACE`
marker. Absence of that marker in a timed-out run is not successful race
verification. These records do not establish the duration of the active test in
isolation. The surrounding workflows were still running when these job logs
were recorded; no workflow-level success, rerun or cancellation is implied.

## Common setup cost

Nine timeout dumps show the active subtest as **runnable**, executing the
modernc SQLite schema parser/rebuilder through:

```text
internal/database.migrateFS (database.go:73)
internal/database.Open (database.go:34)
newGatewayRebindPredecessorFixtureWithEndpointAndLANApprover
  (gateway_rebind_predecessor_test.go:277)
```

Every new predecessor fixture opens a fresh controller database and repeats all
embedded migrations before seeding its own authority. Many higher-level tests
build several such fixtures. The stacks show CPU work in actual schema creation,
not waiting on a Docker timeout or a sleeping fixture clock.

The tenth job, `113462428908`, was runnable in protected progress validation,
startup presence inspection and terminal owned-stop during
`TestGatewayCurrentTerminalEmergencyStopsDespiteCorruptCurrentBundle`. This
remaining work is not removed by a database setup optimization. No claim that
the proposed optimization alone fixes every race-group timeout is made.

## Bounded change and invariants

The local change is confined to an ingress test helper, its isolation
tests and the common predecessor fixture's database-open call. It creates a
fully migrated, unseeded database using the real production `database.Open`
once per test process, then clones a closed, checkpointed snapshot into each
fixture's separate fresh data root. Each clone is opened through production
`database.Open`, retaining its pragmas and normal migration-ledger checks.

Required properties:

- No live database handle, transaction, application, actor, claim or immutable
  rebind history is shared between fixtures.
- Template bytes are immutable and are never a checked-in database snapshot.
  They are generated from the current binary's embedded migrations.
- Initialization and checkpoint/close failures are fatal to the helper's caller.
- Existing database files or sidecars are refused and preserved.
- Concurrent clones have separate writes, rollback, close and reopen behavior.
- Schema, migration versions and the intentional empty profile-head row match a
  fresh production database. No migration, SQL guard or production open path is
  bypassed or modified.
- Migration-specific tests in `internal/database` remain unchanged. The actual
  migration initialization still runs under race instrumentation when the suite
  is run with `-race`.

No test is dropped, skipped, filtered or moved. All 18 ingress groups, Windows
plain mode, Linux race mode, 32-minute budget and strict aggregate gates remain.

## Verification plan and current status

Before helper edits, run these exact existing parents at the published ingress
source, and run the identical selection after the change:

```text
go test ./internal/generatedingress -run '^(TestGatewayRebindProposalAndAdmissionUseRealRepository|TestGatewayRebindCoordinatorCommitsRealSQLAndProtectedBaseline|TestGatewayRebindStageConfigVolumeReplayRejectsIdentityAndCensusSubstitution)$' -count=1 -json -timeout=10m
```

The root's independent `internal/autodeploy/relay_outage_integration_test.go`
budget repair may be present as an unrelated working-tree change. It is outside
this package's test compilation and this change's write scope; a globally clean
checkout is not assumed once that separate work begins.

Record native baseline/candidate outcomes, source hashes, command environment
and elapsed times without a timing-threshold assertion. Exercise fresh-schema
equivalence, pragmas, concurrent isolation, persisted reopen state, rollback and
refusal to overwrite an existing destination. Formatting, scoped vet, security
review and independent final review are required before a local commit.

### Completed baseline and candidate comparison

Both executions used **Go 1.27.0 windows/amd64**, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -buildvcs=false`, and
`GOCACHE=C:/Users/huang/Documents/Projects/Rig/.go-cache-m3`. This is a different
toolchain from earlier local Go 1.26.0 records and the hosted Go 1.26.7 pin.
Neither execution used race instrumentation. No diagnostic source changes were
made between the runs.

| Check | Parents / subcases | Package / command wall seconds | JSON SHA-256 |
| --- | --- | --- | --- |
| Baseline before helper edits | 3 / 5 PASS, zero failures or skips | 33.639 / 40.678 | `5A337ED239BEF7F252C73F8F4E76FDA666AB7D94C49574250E7EF807742E43AE` |
| Candidate with template helper | 3 / 5 PASS, zero failures or skips | 33.503 / 38.954 | `5171625643A08ED018FEEC27B6A9244F651659D133A238FCA8BBEFB10B8A3D62` |

Both commands exited zero and their actual handles were reaped. Artifacts are
under `temp/m3-pr137-database-fixture-{baseline,candidate}-20261009/`, including
`command.json`, `output.json`, `result.json`, toolchain and source records.
The candidate helper SHA-256 is
`0F1562558CA566E12F1A953821983DEFA035E5CF3433B6FD3EE22F1B8F0E2EC4`;
the predecessor fixture SHA-256 is
`34EEA91F453E3871B3C1758BE3072277AEEE31E7E3506E22A5D1A6902F2EF370`.

The individual parent durations were:

| Parent | Baseline seconds | Candidate seconds |
| --- | ---: | ---: |
| `TestGatewayRebindCoordinatorCommitsRealSQLAndProtectedBaseline` | 20.87 | 23.49 |
| `TestGatewayRebindProposalAndAdmissionUseRealRepository` | 1.07 | 0.67 |
| `TestGatewayRebindStageConfigVolumeReplayRejectsIdentityAndCensusSubstitution` | 11.28 | 8.98 |

The overall package difference is negligible. These single local measurements
do not establish a meaningful aggregate speedup, a Linux race duration, or that
the hosted timeouts are fixed. The source change removes repeated migration
execution from this common fixture after its first initialization; other setup,
protected-history and physical-proof work remains.

### Final structural checks and direct setup measurements

After the candidate benchmark, the equivalence test was extended to measure
one warm clone and apply the same schema/static-row/settings comparison to it.
A write to the first clone must also remain absent from the warm clone. The
helper implementation and predecessor call did not change. Final helper/test
file SHA-256 is
`9901ECC35B6156D4DE267D3CDF33B10E014D6DBA883AA41701D096BAE64CF235`.
The benchmark therefore remains evidence for the earlier assertion-only version
of that file; no second benchmark or selective timing retries were run.

The exact structural selection was:

```text
go test ./internal/generatedingress -run '^(TestGatewayRebindDatabaseFixtureMatchesProductionOpen|TestGatewayRebindDatabaseFixtureConcurrentClonesAreIndependent|TestGatewayRebindDatabaseFixtureRefusesExistingControlFiles)$' -count=1 -json -timeout=10m
```

| Platform | Final structural outcome | Fresh production open | Cold template plus clone | Warm clone |
| --- | --- | ---: | ---: | ---: |
| Native Windows | 3 parents / 4 subcases PASS; zero failures/skips; package 1.353 seconds, command wall 6.832 seconds | 390.0333 ms | 406.3904 ms | 12.4327 ms |
| Actual WSL Linux | Same 3 parents / 4 subcases PASS; zero failures/skips; command wall 4.650 seconds | 474.540467 ms | 455.615301 ms | 11.866596 ms |

These are one measurement of each operation per platform, with no asserted
speed threshold. The direct warm setup savings support removing repeated
migration work; they do not override the negligible aggregate difference above
or predict hosted race timing. Cold template initialization still pays the real
migration cost once in each test process.

The Linux binary was built with the same Go 1.27.0 installation using
`GOOS=linux GOARCH=amd64 go test -c ./internal/generatedingress -o <recorded-binary>`
and actually executed in WSL with the same anchored selection, `-test.count=1`,
`-test.v` and `-test.timeout=2m`. Compilation exited zero in 5.413 seconds. This
was a plain binary; no Linux race run is claimed. Exact environment/arguments
are in `structural-wsl-20261009/linux-command.json` under the artifact prefix.

Artifacts under `temp/m3-pr137-database-fixture-`:

| Record suffix | SHA-256 |
| --- | --- |
| `structural-warm-native-20261009/output.json` | `A7E979D1123C66986380603E704778E45D403C0561C973E62A77ED21F27DB34C` |
| `structural-wsl-20261009/linux-output.txt` | `13F1C27C4E4A6B0211ABA0BF0695872B3E1E05A11B79C00C4409A150D7C7489A` |
| `static-20261009/result.json` | `353CD07AD1B2D4A330A0BDB3B13D6E13AA4EE65B74B543389AFEC0D7F275466A` |

`gofmt -d` for the two owned Go files was empty, `git diff --check` passed and
`go vet ./internal/generatedingress` exited zero. The static record's source
hashes agree before and after checks. All processes are terminal. The earlier
structural native run, before the warm-clone assertion, also passed and remains
in `structural-native-20261009`; it is not substituted for the final checks.
The three new parent names are included by the existing `rebind-history`
family selection and excluded from the complementary ingress remainder.

Scoped security review found no remaining source blocker in checkpoint/close,
exclusive copy, isolation or the production-open call. Independent final source
review also passed. Actual hosted reruns of the previously failed groups remain
required. The
live Docker handover/runtime and relay failures are separate acceptance work.
Reverting the two test files restores per-fixture fresh migrations without
changing production data, migrations or deployment behavior.
