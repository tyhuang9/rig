# M4 private generated domain registry and legacy mutation fence

Recorded 2026-10-21. Base:
`63e38af590d75e318a544da82fdfcf4abeb6b0c2`
([draft PR #200](https://github.com/tyhuang9/rig/pull/200)).

## Purpose and boundary

The physical provenance foundation can identify a controller database, protected
root and Docker daemon. This unit adds the private registry and activity accounting
needed to keep existing Manager instances from continuing typed mutations when a
future generated domain reserves conflicting resources.

There is no production reservation caller or ready generated domain in this unit.
The domain factory, startup classifier, route authority and consumer cutover remain
separate work. Successful fencing does not prove Caddy stopped, routes withdrawn,
protected state empty or the gateway absent. Existing `Switch`, `Provision` and
`Recover` behavior and emergency withdrawal construction remain outside this change.

## Registry and construction invariants

- Exact retries join the same retained reservation, including sticky failure.
  No reset or fresh retry state is exposed. Physical root/database collision and
  verified daemon plus fixed namespace collision are evaluated independently.
- A construction ticket is admitted before protected state creation and spans the
  complete `New` assembly, including both physical driver adapters. Publication
  atomically replaces the ticket with the permanent Manager record immediately
  before return. Every failed construction completes its ticket once.
- A pairing epoch closes the constructor roster, waits for already admitted
  constructors, rescans published Managers, observes identities, and drains entered
  inspection/typed activity. The epoch stays closed until final stale publication
  or retained failure. New constructors cannot extend the roster indefinitely.
- Manager value copies share the same view and activity accounting. Observation
  requires the record's original expected view; overwriting the original Manager
  pointer with another value cannot make an older copy disappear from accounting.
- Lock order is registry then shared view. No filesystem operation, Docker command,
  callback, SQL operation or wait runs while those locks are held. Leave is once-only.

## Typed mutation entry

All 27 accepted exported typed mutation entries enter the shared gate before
existing effect/Manager/gateway locks. Related wrappers share gated implementations;
the nested access-startup path uses a private already-entered helper.

With no reservation, the gate performs short activity bookkeeping only. It adds
no filesystem/Docker observation, wait, callback or context validation. Existing
empty-history startup, quarantine and recovery behavior retain this path.

With a reservation, stale or pairing views refuse before effects. Other views
record inspection activity, observe root/ingress and daemon identity outside
locks, then rescan reservations atomically before entering typed activity.
Ambiguous identity, observation failure or cancellation causes conservative refusal.
This private process registry is not a cross-process ownership lock or protection
against a malicious process running as the same operating-system owner.

## Review corrections

Source review identified and resolved:

1. Optional absent ingress identities were not equal on exact retry. Equality now
   handles absent/absent while collision tests retain strict physical comparison.
2. A retained Manager pointer could be overwritten with another Manager, causing
   observation of the wrong shared view. Exact expected-view validation now makes
   that uncertainty a sticky refusal.
3. Publication rejected the constructor's existing protective chmod. Only the same
   retained ingress directory's transition to 0700 is allowed; other path/mode
   transitions remain refused.
4. The pairing epoch ended before all activity drained. It now spans the complete
   drain and final stale publication.

Initial Windows test expectation failures are retained separately from successor
evidence. Tests account for Windows permission semantics and cancellation before
the runner receives buffers. Subprocess scenarios use bounded child execution
and preserve child output; the process-wide registry is never reset for tests.

Static analysis also rejected four new test assignments that copied a lock-bearing
Manager value. The test-only correction copies all 23 non-lock fields explicitly,
including the shared view, and preserves the target lock during retargeting.
Those tests exercise shared references and view accounting; they do not establish
that copying an acquired synchronization primitive is supported. Production code
and existing regression assertions are unchanged by this correction.

## Acceptance status

Independent bounded source/security review is accepted. Report SHA256:
`253324c36f257bd7a36646e3f623c0c35c681b93f0e2de7e1d40ff82d7122afa`.
The original reviewed 26-file manifest SHA256 is
`0afeed3255c35c25882817d47aff210aaee8cf57e4a0400ca692f3548a734226`.
The test-only vet correction has successor manifest
`2eb6c1870f7a08e14bc8a896797164fbb555d736e4ba21b08cef6c92c8563175`
and independent source/security review
`a89669443dea513c566409af9e3fc83c829db6a8a50aa598ba46458d550308f6`.

The focused Windows and native Linux checks each passed 10 unique parents and
15 subcases, with zero failure or skip. The constructor subprocess launcher is
not counted twice. The 27-method source inventory supplements four representative
stale-mutation tests; it is not 27 independent behavior proofs. An immediate
nonblocking assertion in the second-coordinator case does not prove the second
goroutine had reached its wait. Physical symlink coverage belongs to the earlier
provenance foundation, not a new test in this unit.

The Windows run selecting 192 existing affected parents **failed at its unchanged
45-minute package timeout**. It started 34 parents and completed 33; 50 of 54
started subcases completed. No assertion failure or skip was emitted before the
timeout. The remaining 158 parents never started. The active stack was in the
existing Windows protected-path/state-history scan; it contained no registry gate
wait. That single observation does not explain all elapsed time or prove zero
aggregate overhead from this change.

The preserved timeout log SHA256 is
`558a1a26a6ed3f000b7344911689dde22bc51ae992458e9ea315bca0cecd81a6`;
the partial result is
`13ed0a0702595455d0fd0d16839d8afe92647034a8451759c111e9a5553d5214`.
The exact native Linux affected suite passed all 192 parents and 174 subtests,
with no failure or skip. Three supplemental fixture consumers and their two
subtests passed on both platforms. After the test-only vet correction, its two
affected focused parents passed again on both platforms; the unchanged production
and existing-test evidence was retained without repeating the broad suites.

Pinned Go 1.26.7 checks passed: formatting; Windows/Linux ingress vet and five
affected production package builds; Windows no-run and Linux compile-only
`live_docker` checks for ingress and hostd; canonical
`pwsh -NoProfile -File scripts/check-generation.ps1`; and `git diff --check`.
The final 1,053-file Go/module source closure had no additions, removals or edits.
Its SHA256 is
`0b52273feb5500a1b434d55d7de62162844ad08999f0771017d136321b2e9c42`.
The first complete manifest preceded the Linux affected/supplemental checks but
followed the earlier focused checks and Windows timeout. The successor closure
brackets the test repair and final static checks; it does not retroactively
surround those earlier runs. Their original source checkpoints remain separate.

The execution receipt is
`a783fd89a9e75040f98735754b3a933290ebd548c76f7565f457832b7ce6221c`
under `temp/m3-lan-external-db-20261015/m4-domain-registry-fence-verification/`.
It retains the initial wrong-directory Linux harness failure, the Windows
timeout and the initial copylock vet failure separately from corrected checks.
Hosted Docker/race acceptance, exhaustive Windows regression acceptance and M4
completion remain open.
No live controller activation or deployment has been performed.

## Integration with the published CI baseline

The initial registry checkpoint is `64d1f1fe2ba934eac672666d4a87add786e75a32`.
An ordinary merge preserves that history and incorporates published baseline
`a3bc51e04c3d7b79b428f2c12923c72b3646a70d`
([draft PR #206](https://github.com/tyhuang9/rig/pull/206)). The resulting tested
commit is `36ec4f55883d8489ffdc567006d8eb1820bf132b`, tree
`cc86ef12436765295758e623dbfff672d42e8e99`. The complete tree is the exact
union of both parents, without source conflict resolutions. This branch targets
PR #206; the startup database snapshot remains a separate sibling dependency.

On this integrated source, each of Windows and native WSL Linux passed six
parents and 12 unique subtests, with no failures or skips. The selected parents
are:

- `TestGeneratedDomainRegistryFencesLegacyConstructionAndTypedActivity`
- `TestGeneratedDomainTypedOuterAPIFamiliesRefuseStaleViewBeforeEffects`
- `TestGeneratedDomainTypedMutationInventoryIsClosed`
- `TestLiveGeneratedAuthorityAllowsSequentialEffectsScopes`
- `TestLiveGeneratedAuthorityUsesCallerDatabaseAcrossSequentialBindings`
- `TestLiveRuntimeCompositionAcceptsFixtureRunnerPointers`

The first two parents run ten isolated subprocess cases. Those cases were
checked separately and the outer parent was counted once. The tagged hostd
composition parent contributes two cases using local fixture/stubbed recovery;
it does not execute a real Docker journey. Exact discovery and complete
RUN/PASS inventories were verified, with the existing subprocess limits retained
and a ten-minute outer test limit.

All 72 commands passed: 49 aggregate Go formatting checks, target/toolchain
checks, selected compilation/discovery/execution, scoped ingress/liveauthority/
hostd vet, hostd production builds, ingress/hostd live-tag compilation on both
targets, canonical generation and aggregate diff checks. Pinned Go 1.26.7 used
`GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`,
`CGO_ENABLED=0`, explicit target OS and amd64, with count one.

The complete 1,053-file Go/module inventory stayed unchanged before and after
every command. Its SHA256 is
`f7371f4890d3854e8f21fc7a733ef560238e3a6e9eb66f61ee8b11ee98ecbf1b`.
The receipt is
`08606f240f351892a8a1ed82c040001b87b292af76dedcb628fc6c462d3aee74`
under `temp/m3-lan-external-db-20261015/m4-domain-prerequisite-integration-verification/registry-20261021T074416.265733Z/`.
It binds the exact selectors, argv, package directories, source and binary hashes,
raw output, nested child inventories and unchanged final tree. The reusable
verification runner SHA256 is
`1b7b51227aff17a5dcdbe6e9f25440d1fe2d76ae6920ce703cbe7d9e8ab8e733`.

The earlier broad-suite evidence remains a separate checkpoint. This bounded
integration run does not resolve its Windows 45-minute failure or supply live
Docker, race, hosted, classifier, factory or route-cutover acceptance.

## Original-database stage fixture integration

Recorded 2026-10-22. A history-preserving merge incorporates PR #206's
`5e6c130e9b42dfa736c448ea905f76ed1291b829`: six test fixture helpers now use
their original database/repository instead of reopening the database. The
integrated source is `0452a1f4e4e02343fbd9afbe3725177ff4bc271f`, tree
`bb15e61c1861de946cab4f352f3d02f7d7e743ac`. The incoming seven paths (six fixtures and
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
`c6d590128917e1e04f8aecd2f422ee3f3fbfb05e4bae611574ba0901188b37a7`; documentation checks recheck that inventory.

Earlier six-parent integration results and broader suites remain bound to their
recorded revisions and were not repeated. These two local authority controls
and live compilation do not establish real Docker or race acceptance on this
branch. PR #206's route failures remain open; this prerequisite adds no runtime
repair or M4 completion claim.

## Rollback

Before a production reservation caller is introduced, rollback is a source revert
of this private registry and gate wiring. No persisted schema or user-history
rewrite is introduced. Any later factory/cutover change must define its own
rollback and physical-state acceptance; this unit cannot authorize that action.
