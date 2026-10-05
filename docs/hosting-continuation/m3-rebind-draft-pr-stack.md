# M3 rebind draft PR stack

Status: rows 1–25 are published as stacked draft PRs #98–#122. Their base is
draft PR #97, `feature/hosting-m3-two-app-lan-journey` at `d949d2c`. Each
published branch targets its preceding branch with one review scope. Rows
26–37 remain local and require separate publication approval; they have
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
config, and start Docker gates remain unrun on hosted Linux. No draft
publication authorizes merge or deployment.

Rechecked on 2026-10-05: all 25 PRs remain open drafts, every PR after #98
targets the preceding branch, and all 25 are attached to the implementation
chat. No branch in this stack has been merged by this work.

The additional local branch `feature/hosting-m3-rebind-final-config-intent`
implements the complete successor application config plan and protected
sequence-eleven intent on row 37 at `020196d`. Its focused checks, full Go
suite, vet, build, Docker-tag compilation and source review passed. It requires separate publication
approval and is not included in the pending rows 26–37 request.

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
| 36 | `feature/hosting-m3-rebind-stage-start` | `b455d42` | Guarded exact successor start and protected sequence-ten serving proof; local only |
| 37 | `feature/hosting-m3-rebind-stage-start-docker-gate` | `6a83056` | Required hosted Docker direct start, lost-acknowledgment adoption, compensation, and residue gates; local only |
| 38 | `feature/hosting-m3-rebind-final-config-intent` | `020196d` | Complete successor application config plan and protected sequence-eleven intent; local only |

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
| 38 | `020196dc0d1250de315713a97f1206bfce7ba2ce` |

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
These local branches remain
unpublished pending separate authorization.
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
remains pending publication.

These branches do not add a public rebind controller caller, terminal protected
receipt, successor cutover, transfer-aware readers, recovery, or fence release.
Migrations 033 and 034 remain dormant and unreleasable. Publication, merging,
deployment, Linux race, and physical second-device LAN acceptance are separate
gates. The create-before-bind crash windows after the row-24 network,
row-26 config-volume, row-28 data-volume, and row-30 stopped-container effects
remain unresolved and fenced. Rows 32–35 leave the successor stopped and the
active route and SQLite claim unchanged.
