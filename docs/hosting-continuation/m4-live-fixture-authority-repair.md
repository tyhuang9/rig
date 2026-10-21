# M4 live fixture authority repair

## Observed failures

The disposable Docker job on PR #196 at `445dccf` failed before completing two
journeys:

- The Next.js cache fixture submitted `RIG_FIXTURE_*` environment names through
  the real scoped configuration store. These names are reserved for Rig, so
  configuration correctly refused them.
- The notes/database fixture bound a network deployment while its deployment
  status remained `preparing`. Network creation requires a successful policy
  gate and the `applying` state, so the real authority refused before Docker.

The same head's rebind composition job also failed with invalid generated runtime
authority. Several rebind, LAN and process-kill fixtures explicitly injected
runner values while the guarded runtime requires stable runner pointers. The
default-runner repair in PR #199 does not change those explicit injections.

The original logs and exact-head/tree comparisons are preserved under
`temp/m3-lan-external-db-20261015/m4-lifecycle-coordinator-verification/ci-followups-20261020/`:

- `pr196-job-118971706005`: Next.js and database fixtures, raw log SHA-256
  `45ace6fa3eb3288c2d222798e2f15c08ca2279c8234d2ffc42927f385aa7c0b3`.
- `pr196-job-118971706146`: first rebind boundary, raw log SHA-256
  `c0c7f2de5e14f62a38272f149b4853cd888be34a1fa8e10e6e1ace3ea88d25a1`.
- `pr196-job-118971706488`: process-kill child startup, raw log SHA-256
  `33ada59d9b5eb19bccd0d18715bbfbf54f0afbcbeceb8738c08f60cea40aa4ee`.

## Repair

Use `APP_FIXTURE_RUNTIME_SECRET` and `APP_FIXTURE_SLOT_MARKER` consistently in the
Next.js application, fixture configuration, documentation and assertions. Keep
runtime secrets out of image configuration and responses; retain secret clearing
after protected storage.

The test-support `BindNetwork` helper now invokes the real repository policy gate
using the exact bound claim, deployment and findings. It still creates no image
artifact, candidate intent or activated candidate. Candidate sessions retain
their existing start gate.

The affected controller fixtures inject stable pointers. Their command behavior,
process barriers, secret assertions, cleanup safeguards and timeout budgets are
preserved. Production validation and runtime implementation are unchanged.

## Verification evidence

The original non-Docker sequential-effects test failed with
`deployment execution stage is not authorized`; the corrected helper passes the
real network create check. The first six-file checkpoint's evidence is preserved
under `temp/m4-live-fixture-ci-repair/`. Its source receipt was captured after its
tests and is labeled accordingly; it is not a before-test freeze.

Independent review of that checkpoint confirmed consistent secret handling,
unchanged reserved-key policy, genuine policy gating and preserved assertions.
The later runner fixture changes and their final verification are recorded as a
successor checkpoint.

The final successor passed **10 parent tests and two subcases on each of Windows
and native Linux**, with no failures or skips. Saved discovery lists exactly
match the executed parent sets, and raw output records every passing case. The
real generated runtime composition accepts both fixture pointer types. Linux
binaries were cross-compiled with pinned Go 1.26.7 and executed through WSL using
literal argument arrays.

Both targets passed live-tag vet for the affected packages and production hostd
builds. The canonical generation check passed. A read-only gofmt check accepted
the canonical LF text of all eight changed Go files; checkout CRLF bytes were
preserved. Source hashes remained unchanged through the final checks. The earlier
manifest-order and CRLF-only formatter preflight failures are retained separately.

Final executable evidence is in
`temp/m3-lan-external-db-20261015/m4-live-fixture-config-verification/final-runner-canonical-format-20261021/`:

- `receipt.json`, SHA-256
  `17885e5e8344474073335626161a3a006d50adb8b8554669432e7eef37221df2`.
- `commands.json`, exact command arguments, environments and exit codes.
- `source-freeze.sha256` and `source-closure.sha256`, the before/after source
  manifests, plus separate discovery and verbose test logs for every selected
  package and platform.

Documentation uses the frozen lockfile; its workflow, build and accessibility
checks are included in the final publication evidence.

## Limits and rollback

Hosted Docker must verify the complete Next.js, database, rebind, LAN and
process-kill journeys. Local compilation and constructor checks do not establish
that acceptance. Migration container-creation failures are a separate
investigation. Full M4 remains incomplete.

Revert the fixture/application key updates together and revert the test-support
gate and injected-pointer changes with their regressions. No production data,
schema, deployment or immutable history change needs reversal. External
application databases continue to use scoped runtime secrets; no managed database
or Neon provisioning is added.
