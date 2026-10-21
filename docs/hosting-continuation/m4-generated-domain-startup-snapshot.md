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

## Rollback

Before a production caller is introduced, rollback is a source revert of the
new readers and factoring. Existing persisted history requires no conversion or
rewrite. Any later startup classifier or factory integration needs its own
failure handling and acceptance evidence.
