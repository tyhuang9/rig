# M4 rebind fixtures retain original deployment history

Recorded 2026-10-21. Base: `62ba744012d57dec91ff5c67c17c0180c35009a1`
([draft PR #201](https://github.com/tyhuang9/rig/pull/201)).

## Failure and repair

The gateway fixture created its generated deployment through an authority using
one database, then opened a different gateway database. Rebind setup reconstructed
fixed deployment identities and synthesized runtime history in that second store.
Candidate preparation now allocates genuine bound identities, so this reconstruction
failed before the intended rebind assertions. PR #196 jobs `118971706570`
(public-passive predecessor) and `118971706653` (final configuration copy) record
examples of this old fixture failure; neither is acceptance of the repair.

The fixture now opens its database first and retains the original database,
authority and engine. A second application uses the same engine and authority,
preserving their capacity and environment ownership. Duplicate application inserts
and synthetic runtime history are removed. Caller-owned database cleanup remains
with the caller.

The replacement read-only validator requires the retained original candidate
session, completed deployment/job, active head, exact component roster, protected
plan/configuration revisions, artifact and candidate intent bindings. Configuration
export bytes are cleared. The session records its published head only after the
existing publication and completion steps succeed. Validation performs no SQL
mutation, runtime transition, Docker operation or authority creation.

This change affects seven fixture/support Go files. It preserves the original
live test names and their stage, approval, side-effect and cleanup assertions.
Production runtime guards, schema and persisted user history are unchanged.

## Executed local verification

Pinned Go `1.26.7`, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`.

| Check | Actual result |
| --- | --- |
| Windows compiled binaries, exact discovery and execution | 10 parents discovered/RUN/PASS; zero failures or skips |
| Linux amd64 binaries executed through native WSL | Same 10 parents discovered/RUN/PASS; zero failures or skips |
| `go vet ./internal/generatedruntime/liveauthority ./internal/generatedingress` | Passed on Windows |
| `go build ./internal/generatedruntime/liveauthority ./internal/generatedingress` | Passed on Windows |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed |
| `git diff --check` | Passed |

The ten selected parents are the eight default-tag `liveauthority` tests plus
`TestLiveGeneratedAuthorityAllowsSequentialEffectsScopes` and
`TestLiveGeneratedAuthorityUsesCallerDatabaseAcrossSequentialBindings`.
The source/module inventory contains 1,020 files, with exactly the seven intended
Go paths changed and no further source drift during verification.

Local checks cover sequential genuine preparation/binding on the caller's database,
missing/foreign session database, changed candidate application/deployment,
Building-only history, extra component refusal, and unchanged SQL change counts
and protected bundle contents on covered refusals. They do **not** establish
successful published-history validation or separately exercise every identity
predicate in its final SQL query.

Evidence directory: `temp/m3-lan-external-db-20261015/m4-rebind-fixture-history-verification/`
in the local project workspace. Immutable receipt identities:

| Evidence | SHA256 |
| --- | --- |
| Seven-file source checkpoint | `e54ad5e31c6816a8c8be1fc64f304205344c284b29bbe613e9623597ebfde33b` |
| Windows acceptance | `eaa8dbea6c7d7f266e6c7fedec1ab24dce3e89b21c11fc09b339c9fbcda7d6c2` |
| Native Linux acceptance | `950defdb305d06c6ebe8268b0d16c2ec51bc30fac90ef034f513aaf60a55076f` |
| Scoped static receipt | `0c487e4af620a4769990c29cecbdffb9af2a064e5b9c1ce819febb4583cd30a9` |
| Canonical generation receipt | `a60eaba289018ae34c308fc1ca0f2fe7d882ab9c2c6f838e4cfe22f18495deda` |
| Independent security source review | `50c1252f0170b0047db62ab649b7db7731559f6cbb6a14419f203448aacf09ee` |

Earlier timestamp-format test failure and runner-label/discovery harness errors
are retained separately. They are not counted as accepted passes. Source review
caught and resolved the missing full component census, a nondiscriminating census
test and test copies of a lock-bearing session.

## Hosted acceptance and limits

Actual Docker and race checks remain required. Follow the unchanged gateway-v2
and shared-fixture journeys: network stage, public-passive predecessor, final
handover commit/rollback/restart, configuration/data/copy/start stages, cross-store
recovery and runtime replay. Require the original parents and assertions to execute;
compile success and this local refusal coverage do not substitute for those runs.

No manual Docker operation, live controller update or production deployment was
performed for this patch. The validator describes retained fixture history at
its observations; it is not a production capability, live-container attestation
or cross-process fence. Existing sequential session use remains an assumption.
M4 and the overall continuation remain incomplete.

Rollback is a source revert of this fixture repair; it restores the known failing
fixture composition. No database repair, user-data rewrite or runtime rollback
action is introduced.
