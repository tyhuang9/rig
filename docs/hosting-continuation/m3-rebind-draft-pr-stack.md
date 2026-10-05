# M3 rebind draft PR stack

Status: rows 1–25 are published as stacked draft PRs #98–#122. Their base is
draft PR #97, `feature/hosting-m3-two-app-lan-journey` at `d949d2c`. Each
row's branch descends from the preceding row's branch and targets it with one
review scope. Rows 26–27 remain local and require separate publication
approval. The first two hosted row-25 network Docker attempts failed; the
corrected head's required job is queued, so no hosted network-stage acceptance
is claimed. No draft publication authorizes merge or deployment.

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

Each branch has a scoped evidence file in this directory. Local verification
for row 15 passed the full uncached Go suite, vet, Docker-tagged compilation,
502 web tests, web production build, and hostd build. Later rows record their
own exact local checks and limitations in their evidence files. Row 25 passed
the final-source Go suite, vet, web tests and build, and Docker-tagged compile;
its live Docker test only compiled and skipped locally. Rows 26 and 27 passed
their final-source Go suites, vet, builds, and relevant focused checks; row 27's
live test also only compiled and skipped locally. PR #97's hosted checks passed
at its published head. The Docker gates in rows 14, 16, and 25 count as
acceptance only after successful required hosted tests at the relevant head;
row 27 remains local and has no hosted Docker acceptance.

These branches do not add a public rebind controller caller, terminal protected
receipt, successor cutover, transfer-aware readers, recovery, or fence release.
Migrations 033 and 034 remain dormant and unreleasable. Publication, merging,
deployment, Linux race, and physical second-device LAN acceptance are separate
gates. The create-before-bind crash windows after the row-24 network and
row-26 config-volume effects remain unresolved and fenced.
