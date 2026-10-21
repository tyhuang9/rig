# M4 generated domain foundation — local integration checkpoint

## Scope

Move the existing generated candidate owner beside ingress and give it concrete
protected-store dependencies, preparing for a later private domain factory.
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

This introduces no schema, HTTP API, Docker request, environment format or
immutable receipt rewrite. External databases remain application-owned and use
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

These checks perform no filesystem or process operations and grant no effect
authority. Physical root/database identity, daemon identity and collision
exclusion still require the later private construction work. Existing constructor
acceptance and E4 runtime behavior are unchanged.

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

## Remaining implementation boundary

The fixed private domain factory, physical-domain collision checks, gateway
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
together. There are no database migrations, durable receipt changes or live
deployment changes to reverse.
