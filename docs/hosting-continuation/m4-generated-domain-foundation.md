# M4 generated domain foundation — local integration checkpoint

## Scope

Move the existing generated candidate owner beside ingress and give it concrete
protected-store dependencies and verified database/file identity, preparing for
a later private domain factory.
This checkpoint does not implement E5 route publication, predecessor retirement,
accounting transfer, completion, or E6 cold recovery.

The candidate owner, its private ledger and its capability-bearing types now
reside in `internal/generatedingress`. Deployment storage, policy SQL,
migration/image/network authorities and the generic failure barrier remain in
`internal/deployments`. Runtime, executor and live-authority consumers use the
relocated owner. There is one owner implementation and one private ledger.

`GeneratedOwnerStores` contains the repository's exact database, execution guard,
deployment plans and application configuration. Its zero value is invalid;
copies of a configured repository share the issued bundle identity, including
copies made before first issuance. A candidate factory validates and privately
copies the bundle, and keys its registry on the canonical issuing repository.
Caller-owned bundle variables cannot retarget an existing owner or fork its
ledger. The concrete policy method delegates to existing policy evaluation;
it introduces no result writer, arbitrary validator or physical-success setter.

This preserves the existing schema, HTTP API, production Docker operations,
environment format and immutable receipts. External databases remain application-owned and use
scoped runtime secrets. There is no managed database or Neon provisioning.

## Preserved boundaries

- Protected request, claim, attempt, source, configuration, artifact, policy,
  predecessor and whole-roster checks remain at their existing boundaries.
- Writer-reserved SQL transactions, command-entry checks, irreversible
  termination/cleanup uncertainty and retained capacity behavior are preserved.
- The receipt-owned legacy removal barrier stays in deployments, including
  `errors.Is` and wrapped runtime diagnostic compatibility.
- Test-only adapters resolve the package test cycle while calling the real
  runtime engine, live authority and route diagnostic helpers. Opaque network
  and reservation values and session operations pass through unchanged. Input
  environment backing is retained; returned environment bytes are copied.
  Missing, nil or duplicate registration refuses.
- All 3,175 baseline test names remain. The five affected live suites retain
  their 28 names and 315 direct assertions. No CI selector or build tag is hidden.

## Executed verification — 2026-10-21

Verification used pinned Go 1.26.7, readonly modules, CGO disabled, serial
execution and `-count=1`. Linux binaries were cross-compiled and then executed
natively through WSL. Every selected test was discovered and executed.

The focused owner/store/adapter checks passed 72 parent tests and 98 subcases
on each platform, with no failures or skips. These cover all 64 selected
candidate parents, store and legacy barriers, real-engine capacity behavior,
diagnostics and wrapped retirement classification. Live-tag ingress and
controller binaries compiled on both platforms; no Docker journey ran locally.

The broader caller checkpoint passed the following on **each** platform:

| Scope | Parent tests | Cases including subcases |
| --- | ---: | ---: |
| Runtime | 61 | 141 |
| Executor | 53 | 108 |
| Live-authority helper | 7 | 7 |
| Image authority | 10 | 31 |
| Retained lifecycle | 6 | 9 |
| Controller composition | 15 | 72 |
| Core ingress | 80 | 226 |
| Total for this checkpoint | 232 | 594 |

These are bounded suites, not a claim that every repository test ran. Both
targets passed affected-package vet and controller builds. The canonical
generation check (`pwsh -NoProfile -File scripts/check-generation.ps1`),
documentation workflow/build/accessibility checks, formatting and diff checks
passed. Documentation dependencies used a frozen-lockfile install. All 1,462
captured source hashes remained unchanged during the broader verification.

Evidence is retained under
`temp/m3-lan-external-db-20261015/m4-generated-domain-verification/`:

- Focused attempts: `20261021T013508Z` and final `20261021T014027Z`.
  The latter removed only two unused test imports and checked the retirement
  regression. Its freeze and closure SHA-256 both equal
  `8231ab8bba8ff2221778b1af19ec7a1cdd51cf553c8e36e1c4d5010532041622`.
- Broader receipt: `callers-20261021T014317Z/receipt.json`, SHA-256
  `40292611a26e1c49bd5042562d1982a3469fd3c468dd87742b5f084e25433b50`.
- Production relocation review: SHA-256
  `640a4896a2708957e3255199b5fd914785cb6c0edb074146ba51f878d9f68688`.
- Test preservation review: SHA-256
  `e06c0f0e765f60119440b94d25541973edceb065eb8681fd747fa616ac1feed2`.
- Final bounded local-checkpoint review: SHA-256
  `118b54c2385bb533f4b069824cd24946f4788ebff67848cf50fe99686e51235d`.

Historical failed compile attempts and the corrected WSL argument invocation
remain preserved. They are not counted as passing checks.

## Configured store and path provenance checkpoint

The next private-construction prerequisite adds descriptive `ConfiguredForDomain`
checks to the application configuration store, deployment plan store, execution
guard and sealed generated-owner bundle. They require the exact database handle,
supported absolute clean root spelling, exact `<root>/apps` mapping and exact
effects directory. The bundle also revalidates its issued dependencies and the
current object behind its repository pointer. A foreign or zero overwrite of
that repository handle refuses. Unchanged bundle copies retain their provenance.

These path-configuration checks perform no filesystem or process operations and
grant no effect authority. The physical identity prerequisite below adds file
and daemon observations; collision reservation and fencing remain future work.
Existing constructor acceptance and E4 runtime behavior are unchanged.

Pinned Go 1.26.7 verification passed 10 parent tests and 51 subcases on each of
Windows and native Linux, with no failures or skips. Exact discovery preceded
execution. All four affected packages compiled on both targets and passed Windows
vet. All 67 pre-existing functions in the four changed test files remain
unchanged. Independent source review accepted the corrected unit; earlier review
findings and superseded verification attempts remain preserved.

Evidence under `m4-generated-domain-verification/configured-domain-20261021T022109Z/`:

- `verification-receipt.md`, SHA-256
  `55a82771f41cb99adb37e594d73ef3f4ce31a741c695213f67693bff8aaae695`.
- All 1,031 Go/module source hashes remained unchanged; freeze and closure
  manifest SHA-256
  `fd795884a1cc95212151d8e06d6e77c25e1804fff68eb319668d98532bee305d`.
- The sibling source review report has SHA-256
  `50b36bb17d7a2ee53d604f9ad4f05de4e0f8069c832bd7aed714dd02e1047a64`.

## Physical identity and database opening checkpoint

The private identity inspector now combines protected filesystem observations,
the exact opened controller database, and a bounded read-only Docker daemon ID
query. Root/database overlap and daemon/fixed-namespace overlap are independent
collision predicates. They do not yet reserve or fence a domain.

`database.OpenForGeneratedDomain` requires the original live controller owner
lease for the fixed controller working directory. It captures the root and
database file before opening SQLite, binds the actual connection before existing
initialization and migrations, and rechecks before issuing immutable provenance.
The one-shot connector refuses reconnection after a bad connection. Later
validation checks the original connection, file and lease again. Generic
`database.Open` and current daemon startup retain their existing paths.

On Linux, identity observation uses `O_PATH` handles or metadata checks so it
does not drop SQLite's process locks. The ordinary descriptor used to create a
new file closes before SQLite opens it, under a serialized construction gate.
Windows uses protected handle and path identity checks. Unsupported platforms
refuse. Path text containing DSN query/fragment syntax is rejected before file
creation. The existing registered SQLite driver retains the gateway guard UDF.

This relies on the existing protected-directory and cooperating-controller
model. It does not protect against a hostile OS owner changing and restoring a
path entirely inside the opening window, or arbitrary replacement of trusted
in-process driver registrations. DataRoot itself is not required to have the
private working directory's permissions. Validation introduces no application
data writes; opening still performs normal initialization and migrations after
binding, without claiming transactional rollback of earlier successful migrations.

### Executed checks

Final source freeze `generated-domain-provenance-20261021T032018Z` passed:

| Platform | Behavioral parent tests | Subcases | Total passing cases |
| --- | ---: | ---: | ---: |
| Windows | 26 | 27 | 53 |
| Native Linux through WSL | 33 | 35 | 68 |

Exact discovery matched executed tests; none failed or skipped. A separate
child-process helper invocation is excluded from these acceptance counts.
Coverage includes existing owner contention and process-exit release, copied or
closed lease refusal, original database replacement, actual `database/sql`
BadConn/reconnect refusal, capture-window replacement before initialization,
unchanged application rows and migration timestamps from DELETE/WAL inputs,
daemon identity bounds, and independent collision predicates.

The original replaced-open-database regression failed before the correction and
passes in this freeze. The Linux lock regression holds a real transaction on a
second connection, confirms a child process is blocked, performs successful
direct and combined identity inspection, confirms the child remains blocked,
then rolls back and confirms the child can acquire the lock.

Pinned Go 1.26.7, readonly modules and CGO disabled were used. Both platforms
passed scoped vet, production controller builds and live-tag compilation without
execution. The canonical generation check passed once on the final source.
All 26 command exit records are zero. Source and closure manifests match across
all 1,048 Go/module files; the changed-source manifest contains 18 files.

Evidence under `m4-generated-domain-verification/`:

- Final `generated-domain-provenance-20261021T032018Z/verification-receipt.md`,
  SHA-256 `584af6708504a3be6991027ecea0b56e33b62eefcf6d141a08e651edf1f85176`.
- Source/closure manifest SHA-256
  `4bf996dd5959bd4039f352279c9df0d317a58c7c8fbabc4a80679d2858995c11`.
- Changed-source manifest SHA-256
  `33ef0dffa159b357601419a493158f370ec7196c42f1de8b44de56a2619dcbe7`.
- Bounded source security review SHA-256
  `4a4c8893aef855c4deb2ef0af5400f433250da4e0bee2112f4f7fc74a30fbd6b`.

The original failed regression, provisional lock finding, compile/setup failures
and superseded runs remain preserved separately. These local results do not
establish hosted Docker or race acceptance.

## Remaining implementation boundary

The fixed private domain factory, physical-domain collision reservation, gateway
bootstrap/attestation, legacy-view fencing and complete real-consumer cutover
remain required. Ordinary public `Switch`, `Provision` and `Recover` behavior
has not yet been closed by this foundation. The existing candidate constructor
is an intermediate construction path; this checkpoint is not a completed E5
domain API or permission for live activation.

Source inspection also found a pre-existing default-daemon runner identity
mismatch in published baseline `445dccf`. Its independent repair was published
in draft PR #199 at `21bc87cf0e9f559bc6e1fb9b737de15e57f15183` and integrated
locally at `424e6c81e3bba0475cfdd81fe34014532bfc903b`, preserving history.
The published route-contract prerequisite from PR #197 at
`de083a5ee8f99b6c1d0150dbed596fd2cd5353db` is also included.

Combined verification at `m4-daemon-runner-identity-verification/combined-domain-20261021T020422Z/`
passed 16 parent tests and 75 cases on each of Windows and native Linux, with
no failures or skips. A temporary Go overlay restoring the old value default
failed at the real generated-runtime constructor as expected; the fixed default
and injected-runner cases passed. Both targets passed controller live-tag
compilation, vet and builds, and the canonical generation check passed. Source
hashes remained unchanged. This proves constructor compatibility on the combined
source; it does not establish live Docker startup acceptance.

Hosted Docker and race acceptance remain separate from local tests and live-tag
compilation. Local CodeRabbit authentication is unavailable; independent reviews
do not stand in for hosted review. No M4 completion or production readiness is
claimed.

## Rollback

Revert the cohesive relocation, concrete-store bridge and consumer/test updates
together. The dedicated opener and private identity helpers can be reverted as
their own prerequisite unit together with the owner-lease validation changes.
There are no new database migrations, durable receipt changes or live deployment
changes to reverse.
