# M4 generated domain startup database snapshot

Implementation checkpoint: 2026-10-21. Base:
`63e38af590d75e318a544da82fdfcf4abeb6b0c2`,
[draft PR #200](https://github.com/tyhuang9/rig/pull/200).

## Purpose

A later generated-domain startup decision needs a consistent view of retained
database state. This unit supplies a descriptive snapshot through the sealed
owner stores. It does not decide that a domain is empty, ready or recoverable,
and does not activate a factory or change the running controller.

## Intended contract

One read-only transaction on the retained canonical database covers gateway
history, generated runtime heads/deployments/components, candidate intents,
generic generated deployments, generated-image artifact presence and lifecycle
history. Generic deployments and artifacts are independent censuses: both may
exist before a runtime deployment. Candidate intents are read independently so
a restrictive join cannot hide retained state.

Valid generation-zero heads belonging to ordinary applications are omitted;
positive heads for archived applications remain visible. Results contain only
sanitized, sorted descriptive values and are returned after commit. Failure
returns the zero result and never becomes an empty or stable state.

Lifecycle composition uses the existing validators on the same transaction.
It must visit every operation graph even when an earlier operation is incomplete.
A later malformed graph invalidates the snapshot. Binding ancestry provides
generated-effect presence coverage under the existing schema; this is not an
independent binding-table validation or attestation of an arbitrarily modified
database.

The gateway's existing standalone reader keeps its transaction ownership and
finite diagnostic stages. Public transaction readers describe the supplied
transaction; they cannot attest which database created it. The sealed composite
establishes that relationship by constructing its transaction and repositories
from the same retained database.

## Scope and acceptance

The implementation and its bounded source/security review are complete.
Review required preserving exact SQL NULL values in generation-zero heads and
adding controls for the full phase/migration matrix, orphaned candidate history
and a later malformed lifecycle graph. Those source corrections are reviewed.
The final selected verification passed on Windows and native WSL Linux with
pinned Go 1.26.7. Each platform executed exactly 37 parents and 108 subtests with
zero failures or skips: 14 new parents, seven appaccess preservation parents and
16 runtime/candidate preservation parents. Exact discovery and RUN/PASS parity
were checked for all three package binaries.

The cases cover all ten runtime phases and five migration states, exact NULL
validation, independent candidate/deployment/artifact presence, later malformed
lifecycle history, no-write refusal, and a real two-handle WAL snapshot.

Scoped vet, four downstream package builds, hostd builds and live Docker test
compilation passed on both targets. Pinned formatting, canonical
`pwsh -NoProfile -File scripts/check-generation.ps1` and diff checks passed.
All 41 commands succeeded, with the complete 1,051-file Go/module inventory,
binaries, verifier and Git status checked for changes.

Two failed runs remain recorded: the first stopped at formatting; the second
found that the two-operation lifecycle fixture violated the existing one-active-
job index. The correction terminalizes its first job while leaving that job's
lifecycle effect graph incomplete, then creates the second job. The valid
two-operation control and later malformed-graph/no-write assertions remain intact.
Both corrections are confined to tests; the five reviewed production files are
unchanged.

Local evidence is in
`temp/m3-lan-external-db-20261015/m4-startup-snapshot-verification/`.

| Artifact | SHA256 |
| --- | --- |
| Source/security review | `d5183968194a531caff923c8d4a27f778de165fd9bbe1bbaab32f5318167b4b6` |
| Final eight-file source manifest | `9b48794ec538b3c1d4ad8549379ef14127f432d31d85550a23e403eb48b96206` |
| Executable receipt | `2eca398e96aedbfa69940ae1dba17360f5da4de31fa90921aebc6ce9aaa07cf9` |
| Complete source inventory before/after | `7f49b3668c85d16615624a08389cd2025daa3137669b29e3a77f91343f546148` |
| Final executable report | `6c8223897dfd4eedffa9d704f84e01b57c9ed6cb22b2f931fcb568a6b55e01ad` |

Live Docker execution, race detection and hosted acceptance remain unverified.
These local results cover the bounded database reader and selected preservation
cases; they do not complete M4 or establish a ready generated domain.

No registry call, Docker or protected-file observation, readiness classifier,
recovery action, schema migration, production caller, route cutover or database
provisioning is included. Successful SQL inspection alone cannot establish
physical ownership or safe recovery.

## Integration with the published CI baseline

The accepted initial snapshot commit is
`5a0d2449027c01954807434656ca1acb99ddf294`. An ordinary merge incorporates
published baseline `a3bc51e04c3d7b79b428f2c12923c72b3646a70d`
([draft PR #206](https://github.com/tyhuang9/rig/pull/206)) while preserving
both histories. The tested integration is
`a4aefdf87ea4b936aadc78c92ca6f4decc5d3bd7`, tree
`db904b0023bbe717e6ceb40c324c2a937868add7`. Its entire tree equals the exact
union of both parents, without source conflict resolutions. The registry remains
an independent sibling prerequisite.

Each of Windows and native WSL Linux passed these six composite-reader parents
and all nine subtests, with no failures or skips:

- `TestGeneratedOwnerStoresStartupSnapshotRequiresExactCanonicalBundle`
- `TestGeneratedOwnerStoresStartupSnapshotDistinguishesOrdinaryHistoryAndEveryGeneratedCensus`
- `TestGeneratedOwnerStoresStartupSnapshotUsesOneRealSQLiteSnapshotAcrossReaders`
- `TestGeneratedOwnerStoresStartupSnapshotLifecycleSummaryUsesCanonicalValidation`
- `TestGeneratedOwnerStoresStartupSnapshotIsReadOnlyStableAndSecretFree`
- `TestGeneratedOwnerStoresStartupSnapshotFailsClosedForCancellationClosedDatabaseAndMalformedRows`

All 48 commands passed: aggregate formatting, target/toolchain checks, exact
test discovery and execution, appaccess/deployments/generatedruntimestate vet,
hostd builds and live-tag compilation on both targets, canonical generation
and aggregate diff checks. Pinned Go 1.26.7 used `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`, explicit target
OS and amd64, with count one and the unchanged ten-minute test limit.
The same physical SQLite database is retained in the two-handle WAL case.

The complete 1,051-file Go/module inventory was unchanged before and after every
command; SHA256
`638f524a9ff18a9099cdb76340290354dc69cf351e7ba73df41d1a52911585a5`.
The receipt SHA256 is
`efcdcde3f84e6655b3d8d1edc4c9b07ff97e74e820b8e3bd36abcad9a9091622`
under `temp/m3-lan-external-db-20261015/m4-domain-prerequisite-integration-verification/snapshot-20261021T074755.556878Z/`.
It records exact selectors, argv, package directories, complete RUN/PASS
inventories, source and binary hashes, and raw results. Runner SHA256:
`1b7b51227aff17a5dcdbe6e9f25440d1fe2d76ae6920ce703cbe7d9e8ab8e733`.

This targeted run verifies the interaction with the shared baseline. The earlier
37-parent suites remain separate evidence and were not repeated. Live Docker,
race detection, hosted acceptance and M4 completion remain open.

## Original-database stage fixture integration

Recorded 2026-10-22. A history-preserving merge incorporates PR #206's
`5e6c130e9b42dfa736c448ea905f76ed1291b829`: six test fixture helpers now use
their original database/repository instead of reopening the database. The
integrated source is `47d979c66e723cb4ced37335c38fddf7f1703ab2`, tree
`aa74252ce6f05b7b1772ee5319577c41e026197d`. The incoming seven paths (six fixtures and
their evidence document) equal the published inputs; production and other
Go/module sources retain their prior contents. Whole-parent-union audit:
`706231497e8e9a28c45bb920c21dab207e0dbeecb43792712e0637efa745ebd2`.

For this delta, Windows and native WSL Linux each passed these two parents,
with zero children, failures or skips:

- `TestLiveGeneratedAuthorityAllowsSequentialEffectsScopes`
- `TestLiveGeneratedAuthorityUsesCallerDatabaseAcrossSequentialBindings`

Both platforms' live-tag ingress compilation and exact parent discovery passed.
Canonical generation and final diff checks passed. Across both prerequisite
branches this is 16 planned command groups and eight parent executions. The
initial final-diff guard used a mistyped 39-character registry revision; its
failure remains recorded. Correcting only that invocation literal allowed both
final checks to pass. The saved plan and repository source were unchanged.

Evidence is under
`temp/m3-lan-external-db-20261015/m4-domain-prerequisite-integration-verification/stage-delta-20261022T182918Z/rs-post-stage-verification-20261022T140103/`.
Receipt `018-rs-post-stage-receipt.md` SHA256:
`12b2b2299fc7e8d338c7bbdb6d3d358b32b5f68b160b67400c5e6e263e902906`.
Evidence manifest SHA256:
`dedb566d1151398e6ae3dd0c2430591d59dbac36e9d07808f95f37ead0fafe1e`.
The post-integration Go/module inventory is
`ecf25a266697f292dc4a357d29df024e31c0d1953fc95b5194cd1bd8013e8b16`; documentation checks recheck that inventory.

Earlier six-parent integration results and broader suites remain bound to their
recorded revisions and were not repeated. These two local authority controls
and live compilation do not establish real Docker or race acceptance on this
branch. PR #206's route failures remain open; this prerequisite adds no runtime
repair or M4 completion claim.

## Rollback

Before a production caller is introduced, rollback is a source revert of the
new readers and factoring. Existing persisted history requires no conversion or
rewrite. Any later startup classifier or factory integration needs its own
failure handling and acceptance evidence.
