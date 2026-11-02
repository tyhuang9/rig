# Combined M4 recovery acceptance

Status: the reviewed recovery repairs are integrated and focused combined local
verification passed, with one explicitly unverified nested symlink control.
Combined hosted acceptance and M4 completion remain open.

## Purpose and immutable inputs

Exercise created-candidate recovery, superseded-plan reconstruction, controller
restart boundaries and the repaired hosted fixtures in one checkout. Passing
checks on separate branches do not establish their combined behavior.

| Input | Published head | Role |
| --- | --- | --- |
| [PR #271](https://github.com/tyhuang9/rig/pull/271), starting branch | `96d2893e48646d8ba5f77cf664c006a828fd93f0` | Construction fixtures, per-case isolation and complementary CI groups, including #267, #269 and #270 |
| [PR #268](https://github.com/tyhuang9/rig/pull/268), including #263 | `27b812ea93b8fbe20b01f5cd8bc7949c34c79621` | Created-candidate recovery, mixed-creator history and the real completed-job process boundary |
| [PR #264](https://github.com/tyhuang9/rig/pull/264) | `d564616563191f0f33dd8cb57f10e4167e3d8097` | Final LAN child shutdown acknowledgment after joining the process |
| [PR #265](https://github.com/tyhuang9/rig/pull/265) | `148cba70ddae372e7fcd47406417632e4c1dc8a1` | Exact persisted accepted revision during superseded-plan cold reconstruction |
| [PR #266](https://github.com/tyhuang9/rig/pull/266) | `d3ff2027fa3ada18a9e8ab4c02a1ecf581426efa` | Gateway-readiness pause assertions and protected-history controls |

All five heads are ancestors of source integration
`e87ab87e5a20008a47e8b5bd0a2ac84513a31e1d`, tree
`8e56c3431f7d3f5b7acb5b062dda56b89606f194`. Four ordinary merge commits
preserve the published histories: `5a2b13a`, `e554895`, `a4caa0b`, and
`e87ab87`. Their common ancestor is PR #262 at
`e1bfb028bac3a9bbf98b99b75ff68c0a93c5ac98`.

The input map contains 25 distinct paths relative to that common ancestor.
Only three paths have multiple owners: the cold reader, its runtime-boundary
test file, and the controller journey workflow. The integration adds this
evidence document separately from the source integration commit.

## Conflict resolution and safeguards

The only textual conflict was in
`internal/generatedingress/generated_domain_cold_runtime_boundary_test.go`.
The resolution keeps the #263 file as its starting point, including the entire
existing cold-runtime test and its six ordered subtests. It adds the distinct
#265 migration-journey declarations and the necessary import union. All 12
declarations introduced by #263 and all 25 introduced by #265 retain their
complete bodies. The resolved file has the exact 30-import union, with no
duplicate declarations or conflict markers.

The initial formatter invocation used the existing system Go 1.27.0 formatter.
It was followed by the explicitly pinned Go 1.26.7 formatter; the resulting
bytes were identical. This preliminary invocation is retained as a procedural
deviation, not represented as a pinned command or runtime verification.

The cold reader combines without a textual conflict. Persisted accepted-plan
identity remains mandatory; derived supersession does not erase the original
accepted revision. Created-candidate recovery still requires the complete
contiguous durable prefix, exact physical resources, retained provenance and
capacity accounting. The no-effect migration successor is a separate path.
No ownership, protected-state, history or debt check is relaxed by integration.

The controller workflow retains both independent additions: the gateway pause
controls and the seven real process-kill boundaries with their pure controls.
The construction/recovery split and every existing test deadline remain intact.
The new migration 047 and its public mirror retain the exact #263 input bytes;
older migration history is preserved.

External databases remain application-owned and use scoped runtime secrets.
This work adds no managed database or Neon provisioning and performs no live
controller refresh or application deployment.

## Existing hosted evidence

These results establish the named input revisions only, not this combined tree:

- At #263 head `4d18707abc4ce16c80921d8f795c59cb12336686`, tested merge
  `b891bc0881b6a694da78e00487b73e6c9191f1bb`, the
  [Docker lifecycle gate](https://github.com/tyhuang9/rig/actions/runs/41366762945/job/125028441632)
  passed 11 parents and 22 distinct cases with no failure or skip and successful
  cleanup. The [runtime-state race group](https://github.com/tyhuang9/rig/actions/runs/41366762945/job/125028441902)
  passed 30 parents and 108 distinct cases; Windows core checks also passed.
- At #264 head `d564616563191f0f33dd8cb57f10e4167e3d8097`, tested merge
  `975c488f67ec46bcfac828d838a38e1fdfccb125`, both
  [two-app LAN routing/redeploy](https://github.com/tyhuang9/rig/actions/runs/41368156449/job/125032659850)
  and [application-owned TLS database persistence](https://github.com/tyhuang9/rig/actions/runs/41368156449/job/125032660175)
  passed. All six required parents ran and passed once, with no failure or skip.
  Both cleanup gates passed. The database journey includes redeploy, controller
  reopen, stable routing, immutable releases and frontend secret-exposure checks.

The #263 process-kill job passed six boundaries before an unreachable
`after_runtime_success` barrier failed; its following completed-job case did
not execute. #268 repairs that test boundary. Its combined hosted successor is
still required; the earlier partial result is not full process-recovery acceptance.

## Combined local verification

Fresh Windows binaries used pinned Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false` and `CGO_ENABLED=0`.
All 25 input source paths matched their frozen copies before and after execution.
Five packages compiled and every selected parent was discovered before testing.

| Group | Parents | Actual result |
| --- | ---: | --- |
| Focused ingress recovery, capacity, creator and physical-retirement controls | 8 | PASS; the cold-runtime invocation expanded to all six rows, including the required replacement case |
| Database migration, writer and historical-chain controls | 6 | PASS |
| Runtime-state startup snapshot | 1 | PASS |
| Protected plan revision and superseded approval controls | 2 | PASS |
| Pure tagged controller controls | 4 | All parent tests passed; one nested symlink control skipped |
| Complete recovery/accounting plain group | 15 | 36 structured test RUN/PASS pairs, zero failures or skips; 104.05 seconds |
| Complete construction plain group | 36 | 117 structured test RUN/PASS pairs, zero failures or skips; 67.89 seconds |

The 21 focused parent results come from exact top-level RUN/PASS sets and
successful binary exits. Intentional failing child-process output in the
controller controls is not classified as a failed outer test. The one skipped
case is
`TestLiveColdRuntimeRestartManifestRejectsCrossedOrSensitiveShape/symlink_manifest_and_source`:
file symlinks were unavailable on this Windows host. This control remains
unverified locally; no zero-nested-skip claim is made.

The two complete groups used `go test -json -count=1 -timeout=32m` with the
shared runner's exact `-run`/`-skip` selections and no race flag. Their strict
JSON audit requires each expected parent RUN and PASS exactly once, one package
PASS, and no fail or skip Action. Nested Output strings are not additional
events. The exact arguments, selectors and discovery rosters are retained in
`final-v1/verification-plan-v2.json`.

Five affected live journeys were discovered but not executed locally: two-app
LAN, LAN database persistence, migration approval/uncertainty, gateway/TLS
deployment and process-kill recovery. Discovery does not establish Docker
acceptance.

Read-only formatting of all 15 changed Go files and these checks passed:

```text
go vet ./internal/generatedingress ./internal/database ./internal/generatedruntimestate ./internal/deploymentplans
go vet -tags=live_docker ./cmd/hostd
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
pwsh -NoProfile -File scripts/check-generation.ps1
```

The canonical generation script ran through a launcher that verifies pinned Go
is first on PATH. It checked migration mirrors, OpenAPI generation in check
mode, and the exact controller route-contract test. One earlier launcher
preflight failed because it compared an array of matching Go executables with
one path; the repository script never ran in that attempt. The corrected first
resolution check passed. This is an excluded accidental preflight failure,
not an intentional negative-control test. There are 25 accepted command
receipts and that one preserved exclusion.

The five shared CI files exactly match #271's passing offline contract inputs,
so that evidence is reused without repeating unchanged synthetic tests. A new
YAML comparison proves the composed controller workflow equals the common base
with the complete #266 TLS job and complete #268 process-kill job, preserving
all other configuration, deadlines and cleanup.

Documentation uses `pnpm --dir docs check:workflow`, `build` and
`check:accessibility`. Dependencies were installed with the frozen lockfile
offline: 129 packages reused, zero downloaded, lockfile unchanged. Local Node
24.15.0 satisfies the declared engine requirement but differs from CI's
24.19.0; local documentation results do not substitute for that hosted check.

Verification-harness corrections are preserved separately from source changes.
The active JSON analyzer rejects duplicate parents, malformed JSON and global
fail/skip events; its ten isolated controls passed. The original exact-child
label for the cold-runtime invocation was corrected to the actual six-row
coverage. One earlier evidence-sealing script was edited before its original
bytes were retained, so its initial hash is not accepted as an immutable
artifact. The independent root audit recomputed acceptance from the unchanged
native command receipts, raw logs and source copies. The active version-four
summary records that provenance gap; it does not rewrite runtime evidence.

Local proof packets are retained under
`temp/m4-combined-recovery-acceptance-20261102/`:

| Evidence | SHA-256 |
| --- | --- |
| Input ancestry and blob map | `6b0a16feba3be0de5ceb8b016a3b5fcf86667b6521011ead3ef6092f07f82430` |
| Additive conflict-resolution proof | `051eb84e0821b47170e4864cb83db1fb17db01df46706f58fbe07de78e0a2bbf` |
| Resolved test file | `ebd531729d5d8b846f426af201b625d19d8bb5772eaead25b94d3547a5599269` |
| Source integration identity audit | `8bccdbbc20c6c1bbd1ffbf4cb7df3dc2ea6de373baed264b631db8beb691e7a1` |
| Final source manifest | `35877edd8ed6333226b513c79110939c4de5fdd676fdf5311a603d92f31d6100` |
| Active verification summary, version four | `4daee43d59f14c65e27ad58e5d4cf11363b31bcfa5dfe435ea668986d8c47c05` |
| Active command-receipt index | `e7034a378a5424dd68370fb4725180f0d28a025da7eb2b43bd22b53729fcf2a4` |
| Independent qualified native audit | `397649ab38626787116543ef8c15c38823a849e1fb29c79371297ee3976b66c4` |
| Combined CI contract identity | `75b7eead9efdb902a89fa102a1a9d41a3e02f85528f48d182d3b9fd14e4798d9` |

## Remaining acceptance and rollback

The combined head needs actual hosted migration uncertainty, gateway/TLS,
process-kill, LAN/database, Docker lifecycle, Linux race and Windows checks.
The public lifecycle capability, later-worker/E6 guarantees, full M4 acceptance
and the previously documented physical-device/manual accessibility qualification
remain separate open requirements.

This candidate is isolated for draft review and CI. Its input branches and
immutable commits remain available. A future approved rollback should revert
the relevant input through the normal PR path, preserve durable history and
reverify the selected baseline. No GitHub PR merge or deployment is part of
this local integration.
