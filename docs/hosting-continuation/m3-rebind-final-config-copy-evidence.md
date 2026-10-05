# M3 inactive final configuration copy

Status: published as [draft PR #136](https://github.com/tyhuang9/rig/pull/136).
Implementation reviewed and local verification complete; physical hosted
acceptance is pending. The original approved publication head was `bdbb697`.

Base: draft PR
[#135](https://github.com/tyhuang9/rig/pull/135).
Branch: `feature/hosting-m3-rebind-final-config-copy`.
Original base `f555c05898e6b3104fa770958bcb51c8406e04d1` was advanced
additively for the hosted corrections below. Implementation commit
`95e723dad718e84dbfd1b98cc5fd4d2baf4f461d`; integrated verification checkpoint
`7d83b583a6d219fbf58043cef18cb108527434dc` includes the production volume
corrections and migration-journey assertion fix. Later source checkpoints
must retain their own verification results.

## 2026-10-05 hosted fixture correction

PR #135's inherited stage-start gate at `e46185f` failed before a stage could
run: its fixture assigned the successor the same IPv4/port still held by the
live predecessor. All three stage-start cases and cleanup failed; the actual
job log retained three containers in `Created` state. See the exact
[job evidence](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694535).

The reviewed correction at `b938a7a` was merged additively through PR #135.
It uses two distinct addresses on an explicitly enabled, owned dummy adapter
and preserves all runtime and cleanup proofs. This branch's two final-copy
matrix jobs use the same corrected stage fixture, so they also explicitly
enable the new network fixture, verify its prerequisites, and reject owned
adapter residue. No production source changed in this correction.

Integration-tag compilation passed for generated-ingress (1.035s) and hostd
(0.956s), intentionally without executing tests. Workflow YAML and all 32 shell
scripts passed syntax checks using the repository's existing parser and Git
Bash. Both named final-copy tests were discovered and explicitly skipped without
the required opt-ins. The docs workflow and build passed (VitePress 4.85s), and
independent review found no blocking issue in the downstream workflow change.
These local checks do not prove real Docker behavior; new hosted results are
required.

## Purpose and invariants

Copy the complete application configuration already authorized by protected
sequence eleven into the successor's config volume, then retain a create-only
sequence-twelve receipt. The running successor continues serving its original
challenge/404 stage configuration. This supplies the inactive configuration
needed by the later guarded cutover.

- Retain the deployment-effects lease, Manager mutex and gateway OS lock order.
  Repeat complete predecessor, Docker ownership, image, endpoint, network,
  runtime-head and live-stage proof around the checkpoint and receipt.
- Require exact `stage.json` bytes or the exact authorized `stage.json` and
  `active.json` pair. Permit the pinned image's exact `caddy/` directory and
  its optional byte-exact canonical stage autosave. An empty, partial, foreign,
  duplicated, linked, truncated or changed inventory fails without overwriting it.
- At sequence eleven, copy once when only the stage file exists. A new
  invocation can adopt an exact pair after an uncertain acknowledgment, using
  fresh proof. Copy failure never authorizes a receipt in that invocation.
- Sequence-twelve replay requires the exact pair and performs no copy.
  Earlier stage-only replay remains strict. Preserve all prior protected
  bytes, immutable source grants/profiles and prepared-claim fences.
- Bound the inventory TAR read to 192 KiB for this command alone, accommodating
  both configs and one bounded autosave. Keep each individual configuration
  limit and generic command-output limits unchanged.
- Do not reload Caddy, change listeners, probe application responses, update
  routes or SQLite, release a fence, or expose a public rebind caller.

The receipt references the preceding intent and progress digests, retaining
the complete route plan once. Those digests bind the exact authorized config
and prior resource identities; they do not replace future SQL transfer ledgers.

## Verification plan and evidence

| Check | Required evidence | Result |
| --- | --- | --- |
| Inherited baseline | Exact parent implementation full Go suite, vet/build and focused sequence-eleven checks | Parent implementation `020196d` passed the full suite, vet/build and tagged compilation as recorded in the intent evidence. In this new worktree, inherited focused tests passed in 55.374s before Go edits. |
| Driver and TAR inventory | Both orders, large pair over 64 KiB, strict types/paths/metadata/padding, bounded output, sanitized failures and cleared buffers, exact identity bindings | Six focused driver/parser tests passed in 1.777s after review exposed and corrected missing direct stage-to-intent topology comparisons. |
| Coordinator and immutable receipt | Copy, no-copy replay/adoption, absent-effect retry, cancellation/readback errors, checkpoint drift, ambiguous write, forged history, unchanged sequences 1–11 | The final-config-copy group passed in 159.687s. A standalone predecessor-checkpoint regression passed in 11.545s. Old stage-copy success, sequence-eleven success, old-phase rejection and progress checks passed in 66.417s. |
| Integrated verification | Full Go suite, vet/build, tagged compilation, formatting, diff checks, docs build and independent review | Full serialized Go suite, vet, build and tagged compilation passed at `7d83b583`; ingress took 1049.473s. At final source `0aa8d785`, all 68 focused tests passed once without skips in 443.511s, followed by repository-wide vet/build and tagged compilation. |
| Real Docker acceptance | Direct copy and lost-acknowledgment journeys, fresh-Manager replay, unchanged live config/SQL/routes/predecessor and application-request count, exact cleanup | Two named tests and required hosted workflow gates added on this same branch. Both were discovered and skipped locally without explicit opt-in. The draft is published; hosted execution must verify the corrected distinct-address fixture. Compilation and skip are not Docker acceptance. |

Baseline command:

```text
go test -mod=readonly -p=1 -count=1 -timeout=10m -run '^(TestGatewayRebindFinalConfig|TestGatewayRebindStageStartStill)' ./internal/generatedingress
```

Driver/parser command:

```text
go test -mod=readonly -p=1 -count=1 -timeout=10m -run '^TestGatewayRebind(ExactFinalConfigVolumeArchive|FinalConfigCopyDriver)' ./internal/generatedingress
```

Coordinator command:

```text
go test -mod=readonly -p=1 -count=1 -timeout=10m -run '^TestGatewayRebindFinalConfigCopy' ./internal/generatedingress
```

The workflow YAML parsed successfully and its two matrix names match the
two live tests. Formatting and diff checks passed. The existing normal-user
pnpm cache supported `pnpm --dir docs install --frozen-lockfile --offline`
(129 reused packages) and `pnpm --dir docs build` passed in 2.88s. An initial
sandbox offline-install attempt lacked a cached package; it was not counted
as a successful install. CodeRabbit CLI was not authenticated, so it supplies
no local review result. Independent code/security review and QA covered the
coordinator, bounded parser, historical compatibility and immutable receipts.

Windows protected-file tests run with normal filesystem access and the existing
Go cache. An unelevated compile attempt used the default Go cache and failed
with an access-denied setup error; it is not a code test result. Local race
and physical Docker acceptance remain separate from these Windows checks.

## Integrated checkpoints

At frozen source `7d83b583a6d219fbf58043cef18cb108527434dc`, these sequential
commands all exited successfully:

```text
go test -mod=readonly -p=1 -count=1 -timeout=20m ./...
go vet -mod=readonly ./...
go build -mod=readonly -buildvcs=false ./...
go test -mod=readonly -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress
```

Review requested an explicit old-helper restriction to sequences nine through
eleven. A new regression failed before the fix in 14.197s because a direct
old stage-serving proof accepted sequence twelve after the active file was
removed. Existing outer coordinators already rejected this later phase.
Commit `4e57f8f` restores the helper guard. Additive integration of published
ancestor `f6966a1` produces final source
`0aa8d7853c7c34c8c201d2963dd42f17dd725417`.

The incremental final-source verification discovers all 68 tests matching
the following filter, requires each to pass exactly once without a skip,
and then repeats repository-wide vet/build and tagged compilation. All 68
passed once without skips in 443.511s, and the subsequent commands exited
successfully. The earlier full suite is retained as a checkpoint, not
misrepresented as a full-suite run after this small guard change.

```text
go test -mod=readonly -p=1 -json -count=1 -timeout=15m ./internal/generatedingress -run '^(TestGatewayRebindFinalConfig|TestGatewayRebindStageStart|TestGatewayRebindProgress|TestGatewayRebindExactFinalConfigVolumeArchive)'
```

The updated documentation passed `pnpm --dir docs check:workflow` and
`pnpm --dir docs build` (3.20s). Independent final security/integration review
found no additional blocker. Independent QA reviewed the restored phase
restriction and additive integration with no findings. Formatting and the
aggregate diff check passed; a checkout-only CRLF normalization after merge
introduced no Git content change. Hosted Docker remains a separate gate.

## Hosted predecessor checks

The authorized rows 26–38 were published at the exact heads recorded in the
stack receipt. While this local work proceeded, the
[row-27 config-volume Docker job](https://github.com/tyhuang9/rig/actions/runs/37353665780/job/111910512682)
failed with `route_reconciliation_required` after 57.60s. The
[row-31 inherited migration journey](https://github.com/tyhuang9/rig/actions/runs/37353748854/job/111910800922)
failed because its resume assertion required `queued` even though the live
worker had already assigned attempt two. These are failed acceptance results.
Both corrections were implemented and reviewed in their existing PR branches:

- `75bfa3d` (config volume) and `0b44a7c` (data volume) use the existing
  predecessor digest only after validating the exact two mounts. Equivalent
  mount ordering no longer creates false drift; wrong mounts and all other
  changed Docker fields still fail. Before-fix regressions failed, then each
  full focused group passed: config 65.155s and data 87.891s.
- `5c7ba89` corrects only the migration test's immediate resume assertion to
  allow queued, assigned or running for the same job. The subsequent worker
  completion, exact second attempt, migration count, ledger, provenance and
  restart checks remain required. Three existing resume/concurrency tests
  passed in 1.156s; tagged hostd compilation passed in 0.745s.

Those fixes were propagated additively through PRs #124–135 and published;
the first corrected row-38 head was `edf0b8f`. A later
[data-volume Docker job](https://github.com/tyhuang9/rig/actions/runs/37357839673/job/111924676806)
at row-37 head `2c8b8e2` reached the final unchanged-predecessor assertion and
failed there after 66.50s. Four live assertions still used raw mount-slice
equality. Test-only corrections `6cb5dcb`, `62c60a4`, `b65627e` and `c2b4650`
retain independent full predecessor validation and compare the existing
validated digest. Independent review found no weakened ownership, immutable
history, SQL, route, topology or request-count guards. All three existing
mount-order/invalid-mount/non-mount-drift regressions passed with tagged
compilation in each volume gate (3.364s and 3.406s) and on integrated row-38
head `f6966a1512bbfa40468567655bb1fd9a1b3197e7` (2.782s). These test corrections
were propagated and published in the same drafts. Their physical hosted
acceptance is pending; failed or cancelled runs are not passes.

The corrected inherited [migration Docker job](https://github.com/tyhuang9/rig/actions/runs/37359439219/job/111930044124)
subsequently passed at exact row-38 head
`f6966a1512bbfa40468567655bb1fd9a1b3197e7`. Its immutable GitHub revision test
passed, the named migration approval/uncertainty journey passed in 144.11s,
and the owned-resource cleanup step passed. This closes that observed
migration-assertion failure on this head; it does not establish any new
rebind or final-config-copy Docker result.

## Pinned image and autosave correction (2026-10-05)

The earlier publication request at `39b217f` was placed on hold after the
upstream image-directory failure and source-confirmed Caddy autosave behavior
were found. The corrected local source is
`60fa60d384896ab67c3c8a5afff2bae080e44374`; additive merge `dba7439` also retains
the latest PR #135 evidence at `e46185f`. The old requested revision must not
be published as the ready candidate.

This branch integrates the existing draft corrections from PRs #129–135 and
admits the exact pinned `caddy/` directory. After durable stage start, Caddy
can persist one canonical `caddy/autosave.json`. The sequence-eleven/twelve
reader accepts only the approved stage snapshot, never an active snapshot,
alongside exact original config bytes. Snapshot ownership is UID/GID 1000,
mode `0600`; the root-owned directory remains mode `01777`. The 192 KiB
archive limit covers both 60 KiB configs, a bounded snapshot and framing.
No generic output limit, generated config, command, protected byte format,
digest projection, SQL state or fence changes.

The optional snapshot must be byte exact and its directory present. Its
absence alone does not prove a failure, because Caddy can continue serving
after a persistence error; independent live configuration proof remains
mandatory. Unknown paths, duplicates, symlinks, hardlinks, PAX/GNU metadata,
changed owners/modes/content, malformed padding and trailing bytes fail.
Future handover code can select a separate explicit snapshot policy; this
branch's production reader always uses the stage policy.

Verification on the corrected source:

```text
go test -mod=readonly -p=1 -json -count=1 -timeout=20m -run '^(TestGatewayRebindFinalConfig|TestGatewayRebindExactFinalConfigVolumeArchive|TestGatewayRebindStageStart|TestGatewayRebindProgress)' ./internal/generatedingress
go vet -mod=readonly ./...
go build -mod=readonly -buildvcs=false ./...
go test -mod=readonly -p=1 -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress
```

All 76 explicitly discovered top-level tests passed exactly once in 537.126s;
JSON inspection found zero failures or skips. This includes strict seed and
snapshot cases, archive bounds, real sequence-ten/eleven/twelve history
progression, unchanged history, copy/replay/uncertainty, prior stage admission
and phase guards. The seed fixture needed checkout-only CRLF normalization;
its Git blob matches the independently reviewed fixture at `c2d3daf` exactly.
Repository-wide vet/build passed. Tagged compilation passed in 0.762s and
0.807s; no physical effects ran. The earlier full suite remains a historical
checkpoint, not a full suite after this correction.

The independent final source review covered the completed exact parser,
bounded driver, tests, immutable formats and pinned Caddy source and found
no remaining source blocker. No CodeRabbit result is claimed. The corresponding
stage correction's 41 downstream tests passed in 307.338s; existing drafts
#132–135 were updated under their prior publication authorization.
The final evidence and receipt passed `pnpm --dir docs check:workflow` and
`pnpm --dir docs build` (3.64s). Formatting and aggregate diff checks passed.

At head `e46185ff61b3fa519304e5fa0ce8ab9c1a788acb`, PR #135's
[stage-copy job](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694377)
and [stage-start job](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694535)
were still queued. Actual pinned Docker archive metadata and timing remain
unverified until those jobs and this branch's two required Docker journeys
pass. The local Docker engine is unavailable. No Linux race, physical Docker
or second-device pass is claimed for this correction.

## Hosted results and race partition correction (2026-10-05)

At source `b46162ea4a97335976663770b17d2a44612cc3f9`, all nine jobs in
[the M3 Docker workflow](https://github.com/tyhuang9/rig/actions/runs/37385339443)
passed. The direct final-config-copy test passed once in 122.39s
(job 112017115825), and lost-acknowledgment adoption passed once in 91.63s
(job 112017115651). Their logs contain zero failed or skipped named tests,
and both complete resource-cleanup steps succeeded. The inherited stage-start
job 112017115741 passed its direct, lost-acknowledgment and withdrawal tests
in 90.55s, 74.44s and 75.92s, with successful cleanup. These establish only
the named physical boundaries; final handover and cross-store recovery remain
separate acceptance requirements.

Windows verification and the runtime-packages and gateway-v2 race batches
also passed on that source. The
[ingress-remainder race batch](https://github.com/tyhuang9/rig/actions/runs/37385339467/job/112021142430)
failed at its 32-minute Go package timeout (1920.03s). The log contains no
data-race report or failed test assertion before timeout. The active
`TestGatewayRebindStageConfigVolumeReplayRejectsIdentityAndCensusSubstitution/foreign_labels`
subtest had run for seven seconds; its stack was constructing the real SQL
fixture. This is a failed check, not a race pass or proof that all tests finish.

The correction partitions the growing rebind tests into history, config and
resource batches. Gateway-v2 and all other ingress tests remain in their
complementary batches; other runtime packages stay unfiltered. Every test's
complete subtest tree, race instrumentation, timeout, nonempty-pass guard and
required aggregate gate are retained. No application source or fixture changes.

Local verification used the repository's existing YAML parser and the actual
test census:

```powershell
go test -mod=readonly -list '^Test' ./internal/generatedingress
go run -mod=readonly C:/Users/huang/Documents/Projects/Rig/temp/m3-pr136-race-partition-check.go $env:TEMP/m3-pr136-race-test-census.txt C:/Users/huang/Documents/Projects/Rig/temp/m3-pr136-race-partition.sh
wsl -- bash -n /mnt/c/Users/huang/Documents/Projects/Rig/temp/m3-pr136-race-partition.sh
```

All 597 Windows-discovered top-level tests are selected exactly once:
gateway-v2 137, rebind-history 88, rebind-config 55, rebind-resources 78,
ingress-remainder 239. These selection and syntax checks do not run the Linux
race detector; hosted execution must establish each new batch's result.
YAML parsing, the extracted Bash syntax check, and `git diff --check` passed.
`pnpm --dir docs check:workflow` passed, and `pnpm --dir docs build` completed
successfully in 4.36s. The verification scripts and test census are temporary
local evidence; no additional dependency is introduced.
The PostgreSQL/repository-wide race job was still running when this correction
was prepared. No success is inferred for it.

## Remaining work and rollback

M3 still requires three cohesive delivery units, each combining implementation
with its directly coupled verification:

1. Private recoverable final-container handover: exact stopped final identity,
   configuration and network preparation; durable cutover intent; predecessor
   stop/no-old-bind proof; successor route proof; create-only commit/abort
   receipts; bound-resource restart and compensation. Unknown IDs from
   create-before-bind uncertainty remain unresolved, rather than adopted
   through names or labels alone.
2. Cross-store commit and recovery: guarded migration and admission/quiescence
   checks; atomic successor profile/head, complete transfer roster and
   `database_committed` event; shared raw-source/effective-profile resolver
   across upgrade validation, grant/disable and startup; phase-aware snapshots,
   recovery, receipt reattestation and fence release. Prove two sequential
   rebinds, rollback followed by rebind, missing/gapped transfers, no rollback
   after any committed database event, and return of the old network interface.
3. Administrator maintenance and recovery journey: controller/API/UI, distinct
   configure/rebind approvals, affected-app preview, truthful recovery state
   and URL withholding. Include two applications, frontend/API, redeploy,
   restart, isolation and real second-device DHCP/interface-change proof.

Physical cutover, cross-store transitions and public callers remain gated on
their own executable evidence. External databases remain application-owned
and configured through scoped runtime secrets. No managed database or Neon
provisioning is included.

Keep this private entrypoint unwired. Reverting this branch does not authorize
deleting uncertain Docker resources or protected receipts. Retain evidence and
require exact recovery proof. Publication needs separate user authorization;
no merge or deployment is authorized.
