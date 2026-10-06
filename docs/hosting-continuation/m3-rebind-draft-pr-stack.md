# M3 rebind draft PR stack

Status: rows 1–38 are published as stacked draft PRs #98–#135. Their base is
draft PR #97, `feature/hosting-m3-two-app-lan-journey` at `d949d2c`. Each
published branch targets its preceding branch with one review scope. Rows
26–38 were published after explicit user approval on 2026-10-05; they have
inherited the row-25 race-partition head `9ad7f78` through additive merges. The
first four hosted row-25 network Docker attempts failed; the fifth and sixth
passed the required live network-stage test and residue gate at `ad8d3e2` and
`f91140c`. A later replay parity assertion failed at `a5b4d10`. The corrected
hosted job passed the named test and residue gate at `fd68ab8`; the same gate
also passed at evidence head `f1707e9`. This is network-stage acceptance on
hosted Linux at those tested heads. At `6023a0f`, the network-stage Docker gate
and repository-wide Linux race passed, but the dedicated ingress race job hit
its 32-minute timeout. Head `9ad7f78` partitions that race job without removing
tests. Its fast check, network-stage gate, all three dedicated race batches
and stable aggregate passed. The separate repository-wide race job also
passed at 2026-10-05 17:34:51 UTC. The later volume, container,
config, and start Docker gates were running or queued at the publication
checkpoint; their acceptance remains pending. No draft
publication authorizes merge or deployment.

Rechecked on 2026-10-05: all 25 PRs remain open drafts, every PR after #98
targets the preceding branch, and all 25 are attached to the implementation
chat. No branch in this stack has been merged by this work.

The branch `feature/hosting-m3-rebind-final-config-intent`
implements the complete successor application config plan and protected
sequence-eleven intent on row 37 at `020196d`. Its focused checks, full Go
suite, vet, build, Docker-tag compilation and source review passed. It was
published as draft PR #135 at documentation head `f555c05` under the user's
explicit rows 26–38 authorization. See the publication receipt below.

| Order | Branch | Scope commit | Review scope |
| --- | --- | --- | --- |
| 1 | `feature/hosting-m3-rebind-lineage` | `985552f` | Dormant migration-033 lineage and SQL fence |
| 2 | `feature/hosting-m3-rebind-runtime-fence` | `936d236` | Runtime and gateway effect fence |
| 3 | `feature/hosting-m3-rebind-deployment-drain` | `5fcdbb9` | Cross-process deployment-effects admission |
| 4 | `feature/hosting-m3-rebind-startup-admission` | `2771f05` | Startup recovery admission before gateway effects |
| 5 | `feature/hosting-m3-rebind-predecessor-attestation` | `21d5db6` | SQLite and protected predecessor proof |
| 6 | `feature/hosting-m3-rebind-quiescence-census` | `24bbffe` | Read-only deployment/job census |
| 7 | `feature/hosting-m3-rebind-cutover-contract` | `d51eff2` | Required cutover, recovery, and acceptance contract |
| 8 | `feature/hosting-m3-rebind-successor-preflight` | `e4861a0` | Prepared-claim successor network preflight |
| 9 | `feature/hosting-m3-rebind-preclaim-attestor` | `4bd7e8a` | Zero-claim SQLite/protected predecessor proof |
| 10 | `feature/hosting-m3-rebind-docker-ownership` | `70a7c95` | Passive exact Docker predecessor ownership |
| 11 | `feature/hosting-m3-rebind-admission-rehearsal` | `94e09ec` | Immediate-transaction preclaim rehearsal |
| 12 | `feature/hosting-m3-immediate-rollback-poison` | `61c3d38` | Discard uncertain SQLite connection after rollback failure |
| 13 | `feature/hosting-m3-rebind-prepared-docker-attestation` | `b0bd439` | Prepared-claim Docker proof and zero app probes |
| 14 | `feature/hosting-m3-passive-docker-live-gate` | `cdf00f5` | Required hosted Docker request-counter gate |
| 15 | `feature/hosting-m3-rebind-preclaim-successor` | `112723a` | Zero-claim successor network preflight |
| 16 | `feature/hosting-m3-rebind-public-passive-docker-gate` | `b6f9609` | Required hosted Docker gate for public passive rebind attestors |
| 17 | `feature/hosting-m3-rebind-successor-identity` | `150623a` | Generation-scoped private successor resource identity |
| 18 | `feature/hosting-m3-rebind-successor-intent` | `e9e5ace` | Protected successor intent shape and digest |
| 19 | `feature/hosting-m3-rebind-protected-intent` | `9c2dae3` | Create-only successor protected intent history |
| 20 | `feature/hosting-m3-rebind-sql-ledger` | `2eaf35d` | Dormant migration-034 rebind claim and fence |
| 21 | `feature/hosting-m3-rebind-effect-boundary-attestor` | `58523f4` | Prepared rebind effect-boundary proof |
| 22 | `feature/hosting-m3-rebind-progress-history` | `de25a22` | Append-only pre-effect rebind progress |
| 23 | `feature/hosting-m3-rebind-progress-attestation` | `ad9471a` | Progress bound into the effect-boundary attestation |
| 24 | `feature/hosting-m3-rebind-guarded-nonterminal` | `542ce48` | Private successor ingress-network Docker effect |
| 25 | `feature/hosting-m3-rebind-network-docker-gate` | `f00f911` | Required hosted Docker test of the exact network effect and Linux bridge delta |
| 26 | `feature/hosting-m3-rebind-config-volume-stage` | `307c8f3` | Private successor config-volume Docker effect and protected sequence-four binding |
| 27 | `feature/hosting-m3-rebind-config-volume-docker-gate` | `742124d` | Required hosted Docker test of the exact config-volume effect and replay |
| 28 | `feature/hosting-m3-rebind-data-volume-stage` | `9ccf6a9` | Private successor Caddy `/data` volume effect and protected sequence-five binding |
| 29 | `feature/hosting-m3-rebind-data-volume-docker-gate` | `fcc7b72` | Required hosted Docker test of the exact data-volume effect and fresh-Manager replay |
| 30 | `feature/hosting-m3-rebind-stopped-stage-container` | `1dd5112` | Private stopped successor stage-container create and protected sequence-six binding |
| 31 | `feature/hosting-m3-rebind-stopped-stage-container-docker-gate` | `c156e66` | Required hosted Docker test of the exact stopped container and fresh-Manager replay |
| 32 | `feature/hosting-m3-rebind-stage-config-intent` | `57a80f8` | Protected sequence-seven fixed config intent and strict empty config inventory |
| 33 | `feature/hosting-m3-rebind-stage-config-copy` | `0143b51` | Guarded sequence-eight exact config copy and protected readback receipt |
| 34 | `feature/hosting-m3-rebind-stage-config-docker-gate` | `1ba86c0` | Required hosted Docker direct-copy and lost-acknowledgment adoption gates |
| 35 | `feature/hosting-m3-rebind-stage-start-intent` | `3dfee6b` | Protected sequence-nine exact start intent; no Docker start or listener effect |
| 36 | `feature/hosting-m3-rebind-stage-start` | `b455d42` | Guarded exact successor start and protected sequence-ten serving proof |
| 37 | `feature/hosting-m3-rebind-stage-start-docker-gate` | `6a83056` | Required hosted Docker direct start, lost-acknowledgment adoption, compensation, and residue gates |
| 38 | `feature/hosting-m3-rebind-final-config-intent` | `020196d` | Complete successor application config plan and protected sequence-eleven intent |

## Verified local ancestry

On 2026-10-05, `git merge-base --is-ancestor` passed for every consecutive
pair from row 25 through row 38. The integrated local heads below include
the additive ancestor corrections. Row 38's documentation may advance after
its code verification; `020196d` identifies its implementation.

| Row | Integrated local head |
| --- | --- |
| 26 | `dfa7f4d4018d7359e3d6c22a01af552381b9ba4c` |
| 27 | `7cb3fd556cbdef8a72b4d1a85e90461bbd257260` |
| 28 | `d3aef3d52cb7187e991f72ebcfc0b4ec0943956b` |
| 29 | `d7ce0c6a2d78507cc4cefb92517616a225590f25` |
| 30 | `89c031e3013beaffed72438cedbc631a095b7369` |
| 31 | `a077cebefe1afe2809cecdaae4b7875d48b865c3` |
| 32 | `67b1cd580cfbcbe196c757cdd10a5e675a1adca1` |
| 33 | `1b9bdf64283838c9642a2b7160d2ccd931d3f928` |
| 34 | `a0fb9b49f6bcd2a816faa5ad7c8e3d8485b88107` |
| 35 | `eaaff4e71affa07867f61d7d5ec6b39f541180e5` |
| 36 | `b144f02f184735c1df6c1f8ff151ed7a3a5e962f` |
| 37 | `98a716dd5f158335cee8c2e9ca3a0907364c4b8e` |
| 38 | `f555c05898e6b3104fa770958bcb51c8406e04d1` |

## Rows 26–38 publication receipt

On 2026-10-05 the user authorized these thirteen drafts. The exact integrated
heads above were pushed atomically without rewriting history. GitHub and
remote-ref reads then verified every head, base, open state and draft flag;
all thirteen PRs were attached to the implementation chat. Row 26 targets
row 25's branch from PR #122; every later PR targets its immediate predecessor.
No merge or deployment was performed.

| Row | Draft PR |
| --- | --- |
| 26 | [#123](https://github.com/tyhuang9/rig/pull/123) |
| 27 | [#124](https://github.com/tyhuang9/rig/pull/124) |
| 28 | [#125](https://github.com/tyhuang9/rig/pull/125) |
| 29 | [#126](https://github.com/tyhuang9/rig/pull/126) |
| 30 | [#127](https://github.com/tyhuang9/rig/pull/127) |
| 31 | [#128](https://github.com/tyhuang9/rig/pull/128) |
| 32 | [#129](https://github.com/tyhuang9/rig/pull/129) |
| 33 | [#130](https://github.com/tyhuang9/rig/pull/130) |
| 34 | [#131](https://github.com/tyhuang9/rig/pull/131) |
| 35 | [#132](https://github.com/tyhuang9/rig/pull/132) |
| 36 | [#133](https://github.com/tyhuang9/rig/pull/133) |
| 37 | [#134](https://github.com/tyhuang9/rig/pull/134) |
| 38 | [#135](https://github.com/tyhuang9/rig/pull/135) |

CI was running or queued at this checkpoint, with no observed failure. That
status does not establish new-head hosted acceptance. The full per-check
publication snapshot is retained in the local publication receipt; the PR
checks are authoritative for subsequent results.

## Published CI corrections after rows 26–38

The first hosted config-volume run failed at the production effect boundary;
Docker can return the same exact mount set in a different order. Reviewed
config/data-volume corrections `75bfa3d` and `0b44a7c` compare the existing
validated predecessor digest, retaining exact mount and all other field
checks. Their failing regressions became passing and both full focused groups
passed. The inherited migration journey also exposed a worker scheduling race
in its immediate resume-status assertion; test-only `5c7ba89` accepts queued,
assigned or running while retaining subsequent exact-attempt, ledger and
restart requirements. These corrections were propagated and published through
the existing drafts without rewriting history.

The next [data-volume Docker attempt](https://github.com/tyhuang9/rig/actions/runs/37357839673/job/111924676806)
at row-37 head `2c8b8e2` failed after staging/replay in an unchanged-predecessor
assertion that still compared mount slice order directly. Four reviewed
test-only corrections now independently validate both complete observations
and compare the validated digest. The integrated three-regression check
(mount ordering, invalid mounts, and non-mount drift), including live-tag
compilation, passed in 2.782s. Every published head, immediate base and draft
state was verified. Hosted reruns remain pending, and the failed attempts are
retained as failed evidence in the scoped final-config-copy evidence file.

| Row / draft | Published correction head |
| --- | --- |
| 27 / #124 | `6cb5dcb392093e51a79403692de3c788b8ca715c` |
| 28 / #125 | `d45998be35637d5d34f68a8562f9b4b82562aa17` |
| 29 / #126 | `37c449bf7ee541e1ea1f055966d4575860efce18` |
| 30 / #127 | `5ed939de8f909464fc1e3f55d50e0c841808fd21` |
| 31 / #128 | `cffa0a055c07a86977ddd73e44d6335a8bc97e36` |
| 32 / #129 | `7b95a75f93438b3b6ea3f72e974dd963462a8c51` |
| 33 / #130 | `b3d8621831370b3f6bd9d807e7831b6a4123ab1e` |
| 34 / #131 | `32e377a2752fadd46414a8dc77b241ebcd401264` |
| 35 / #132 | `d47ba3ace473ab1237b28172d9d3891383b2d3c7` |
| 36 / #133 | `094c9f1ffda54904d455e7019c0e990c0f4a1a02` |
| 37 / #134 | `3f7669c9476ec37017ed0755f7191702202c8044` |
| 38 / #135 | `f6966a1512bbfa40468567655bb1fd9a1b3197e7` |

Row 26's original publication head is unchanged. No merge or deployment was
performed. The next final-config-copy implementation is local and requires
separate publication authorization.

At row-38 head `f6966a1`, the corrected inherited
[migration Docker job](https://github.com/tyhuang9/rig/actions/runs/37359439219/job/111930044124)
passed both required named tests; the live migration journey took 144.11s
and its owned-resource cleanup passed. Rebind gates remain separate and
pending at this checkpoint.

## Additive archive/autosave correction receipt (2026-10-05)

Existing authorized drafts #129–135 received the reviewed pinned-image
directory correction. Drafts #132–135 then received the strict post-start
Caddy autosave correction. Each remote head, original base and open draft
status was verified after publication; no source history was rewritten.

| Draft | Verified published head |
| --- | --- |
| #129 | `f2614d73e8a6592f0c5196c2daab3e8591a6ad5f` |
| #130 | `496e3d64977a8decac949fc1ba00f5286751c941` |
| #131 | `b8b50eee2b72b4fda5fe966ef45a80e8de895a46` |
| #132 | `8801a091a0a5b7d9f23b7c72bf5731efb8bc9226` |
| #133 | `b236cda27c78e5fcf5dca3cba3bd3f99f0bff272` |
| #134 | `5d497101cfb1206ea603a8100b73ed7a5e4f21d6` |
| #135 | `e46185ff61b3fa519304e5fa0ce8ab9c1a788acb` |

The [final-intent evidence](./m3-rebind-final-config-intent-evidence.md)
records 41 downstream tests passing in 307.338s with zero failures/skips and
the still-pending hosted gates. The separate, unpublished final-config-copy
candidate now has [76 passing regressions](./m3-rebind-final-config-copy-evidence.md#pinned-image-and-autosave-correction-2026-10-05)
at corrected source `60fa60d`. Its older `39b217f` publication request was put
on hold; publication of the corrected candidate needs a replacement request.
The private handover is also unpublished. No merge or deployment occurred.

## Acceptance and remaining work

Each branch has a scoped evidence file in this directory. Local verification
for row 15 passed the full uncached Go suite, vet, Docker-tagged compilation,
502 web tests, web production build, and hostd build. Later rows record their
own exact local checks and limitations in their evidence files. The table's
commit column identifies each original branch slice; subsequent corrective
commits remain in the same branches. Row 25's integrated route correction
passed the serialized full Go suite, vet, build, Docker-tagged compilation,
and docs build. Earlier web typecheck, 502 tests, and production build passed
before the Go-only correction. PR #97's hosted checks passed at its published
head. The first four hosted row-25 Docker attempts failed and remain in the
row-25 evidence file. The [fifth hosted network-stage job](https://github.com/tyhuang9/rig/actions/runs/37260595415/job/111606812391)
passed the required named live test at code head `ad8d3e2` in 54.89 seconds,
and its always-run residue step passed. This establishes the private network
stage on that hosted runner.
The [corrected hosted network-stage job](https://github.com/tyhuang9/rig/actions/runs/37264835255/job/111619317032)
passed the named test in 51.70 seconds and its always-run cleanup at `fd68ab8`.
The [evidence-head job](https://github.com/tyhuang9/rig/actions/runs/37265095730/job/111620115206)
also passed both in 46.06 seconds at `f1707e9`. The race package passed at
`a5b4d10` in 1116.225 seconds, then timed out at the 24-minute Go limit on
`f1707e9` without a race report. The parent repository-wide race job was
cancelled at its 60-minute job ceiling. At `6023a0f`, the named network-stage
test and cleanup passed, and repository-wide Linux race verification passed;
the dedicated ingress race package still hit its 32-minute timeout. Rows
26–37 now contain the additive correction from `9ad7f78`: an exhaustive
three-batch race matrix with a stable aggregate check, and a bounded wait for
the frontend status-focus effect. Row 25 locally passed all 502 frontend
tests, its production build, both ingress partitions (453 tests discovered
and run exactly once), the other eight runtime packages, and docs build.
The dedicated race matrix, stable aggregate and network-stage Docker gate
passed on `9ad7f78`; the separate repository-wide race job also passed.
These branches were subsequently published under the authorization and exact
heads recorded above. The following paragraphs retain their local checkpoints.
Rows 26 and 27 passed their final-source Go suites,
vet, builds, and relevant focused checks before this network-route correction
was integrated. Row 28 passed its original final-source Go suite, vet, build,
and Docker-tagged compile. Row 27's live test compiled and skipped locally;
its hosted Docker acceptance remains open. Row 29 passed its original
final-source Go suite, vet, build, and Docker-tagged compile; its live test
compiled and skipped locally. Its hosted Docker acceptance remains open.
Row 30 passed its original final-source Go suite, vet, build, Docker-tagged
compile, focused progress and stage-container tests, formatting, and diff
checks. Row 31 passed its original final-source Go suite, vet, build,
Docker-tagged compile, formatting, and diff checks; its live test compiled and
skipped locally. The stopped-container hosted gate remains open. Rows 32 and
33 passed their original final-source Go suites, vet, build, Docker-tagged
compile, and focused intent/copy tests. Row 34 passed its original serialized
full Go suite, vet, build, Docker-tagged compile, and focused copy tests. Its
two live tests compiled and skipped locally; hosted Docker acceptance remains
open. Row 35 passed its original focused intent tests, serialized full Go
suite, vet, build, and Docker-tagged compile. It records start intent without
starting Docker or publishing a listener. Row 36 adds guarded sequence-ten
serving proof with direct-start, lost-acknowledgment, replay, drift, and
compensation tests; its scoped evidence records local checks. Row 37 adds
three named hosted Docker tests and an always-run exact-resource residue scan.
Its live tests compiled and skipped locally; physical Linux Docker acceptance
remains pending completion of the published hosted checks.

These branches do not add a public rebind controller caller, terminal protected
receipt, successor cutover, transfer-aware readers, recovery, or fence release.
Migrations 033 and 034 remain dormant and unreleasable. Publication, merging,
deployment, Linux race, and physical second-device LAN acceptance are separate
gates. The create-before-bind crash windows after the row-24 network,
row-26 config-volume, row-28 data-volume, and row-30 stopped-container effects
remain unresolved and fenced. Rows 32–35 leave the successor stopped and the
active route and SQLite claim unchanged.
