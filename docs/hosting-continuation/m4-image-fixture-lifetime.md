# M4 image-authority fixture lifetime

## Observed failure

On PR #233 at `c4fe39ebaa82ae35752163da97f632e8f4ce54c8`, the
[Docker hosting job](https://github.com/tyhuang9/rig/actions/runs/40250689834/job/121341702702)
ran synthetic merge `57f55af24d93d061fb5d4024d9b46692db455f81`.
`TestLiveGeneratedImageAuthority/BuildAndReuse` passed in 11.06 seconds.
The next subtest, `AuthorityLostBeforeBuildLoad`, failed in 0.68 seconds during
construction with `stage=initial_classification reason=disposition`, before its
authority-loss hook was installed. Raw log SHA-256:
`18de15218493337d0ecedb996f1124be03a3ef8126dae70b28f432d430b554fc`.

## Cause and change

The first successful construction publishes a legacy manager record in the
process registry. Each later construction rechecks the filesystem identities
of all retained records before checking conflicts with the new input. The live
fixture used `t.TempDir()`, so the first subtest removed its root before the
second subtest began. The registry correctly refuses that missing identity.

The fixture now uses the already imported
`generateddomaintest.NewRetainedRoot(t)`. Its existing package `TestMain`
removes retained roots after all tests and their cleanup callbacks finish.
The legacy identity recheck uses the filesystem identities and Docker daemon
identity; it does not query the closed SQLite handle or inspect the removed
image/container resources.

This is one test-line change. Both subtest names, fixture bodies, authority-loss
hook, durable-state assertions, and Docker/database/lease cleanup are preserved.
Production registry checks and APIs remain unchanged.

## Verification and acceptance

The delivery evidence records the exact one-line diff, tagged compilation and
outer-test discovery, scoped vet, canonical generation verification, and
documentation checks. Listing the outer test does not execute its dynamic
subtests. Local verification does not establish Docker behavior.

Hosted acceptance requires the existing image-authority parent and both named
subtests to run and pass. Other PR #233 failures, including the cold retained
gateway consistency refusal, remain separate open work. This fixture correction
does not establish full CI or M4 acceptance.

## Rollback

Reverting the one-line fixture change restores the prior temporary-root behavior.
No production configuration, database migration, or deployment is involved.
