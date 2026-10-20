# Ordinary Compose apply acceptance gate

Local fixture verification recorded on 2026-10-18. The first hosted Docker run remains pending.

## Purpose and boundary

This test-only M4 branch adds an ordinary hostd Compose apply journey and a dedicated hosted Docker workflow on coordinator baseline `3160faa51a01e0691d093f6e46c07dfd50356006` (draft PR #189). Existing generated-runtime Docker gates do not establish this path. No production code, database schema, public lifecycle capability or controller installation changes are included.

The fixture uses the actual controller runtime composition, scoped job admission, persistent job/deployment binding and original Claim, repository, Compose executor, protected configuration stager and process runner. It forwards real process requests and results; it does not substitute an authorization callback or a successful Docker response. A fresh local application and SQLite state isolate every case. The disposable app uses one pinned Node image, a public marker and an ephemeral runtime secret. It has no published port, host mount, persistent volume or privileged option.

## Executable cases

`TestLiveHostdComposeApplyJourney` runs four named cases in order:

| Case | Required observation |
| --- | --- |
| `Success` | One config and one up; actual healthy container and HTTP marker; identical validated/effective model hashes; `deployment_completed`; succeeded job and deployment; immutable binding and provenance. |
| `UnhealthyRetainsRuntime` | Actual nonzero up outcome; failed job at attempt 1 with `compose_apply_failed`; deployment remains applying; unhealthy running container retained; no compensating runtime deletion or successful resume. |
| `AttemptLostBeforeUp` | Exact fixture database trigger changes the job attempt at the applying gate; config executes but up never does; original authority is refused; deployment/provenance retained and no container/network created. |
| `AttemptLostAfterUp` | Real acknowledged up followed by an exact fixture attempt change; fresh observation rejects lost identity; applying state and healthy runtime remain; one up only; terminal resume cannot reissue the command. |

The attempt changes are adversarial fixture writes used to exercise authority loss. They do not prove a legal production writer can bypass the admission scope, nor do they provide a general attempt-update API.

Three pure parents verify command classification, evidence redaction and ownership-manifest validation. The workflow checks exact parent/subcase discovery and rejects missing, duplicate, failed or skipped expected tests. Separate finite receipts require the expected command counts, process outcome, job/deployment state and cleanup result for every case.

## Secrets, isolation and cleanup

Live execution requires an explicit environment flag, Linux and the default local Unix Docker context. Docker endpoint/configuration overrides are rejected. The fixture image is `node:22-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5`.

Private config output is allowed to contain the secret while being validated. Process arguments/environment, up output, durable state and uploaded artifacts must not expose it. Errors and receipts use finite values, and uploads use an explicit file allowlist. HTTP verification disables proxies and redirects and validates exact container/network/endpoint membership before probing.

The worker must finish before resource cleanup and database closure. Failed join retains resources. Create-only manifests bind test run, case, application, project, service and network. Cleanup checks full immutable resource IDs and all ownership labels, and removes only exact containers and verified empty networks. It never prunes Docker resources or removes images or volumes. Workflow cleanup repeats these ownership checks. Hard termination may prevent cleanup; this gate does not claim otherwise.

## Verification evidence

The final three source files were frozen and checked before and after execution with Go 1.26.7, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`.

| Check | Actual local result |
| --- | --- |
| Windows pure fixture contracts with `live_docker` tag | 3 parents passed; no missing, duplicate, failed or skipped tests |
| Native Linux pure fixture contracts with `live_docker` tag | Same 3 parents passed; no missing, duplicate, failed or skipped tests |
| Windows and Linux live-tag compilation | Passed; actual live journey not selected |
| `go vet -tags live_docker ./cmd/hostd` | Passed |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed |
| `gofmt -l` and `git diff --check` | Passed |
| Workflow YAML and embedded Bash syntax; receipt contract structure | Passed; actual jq predicates require hosted execution |

The exact local selector was `^(TestHostdComposeLiveObserverClassification|TestHostdComposeLiveEvidenceRedaction|TestHostdComposeLiveOwnershipManifest)$` with `go test -tags live_docker -json -count=1 -timeout=3m ./cmd/hostd -run <selector>`. Linux used a cross-compiled live-tag binary executed natively under WSL and converted with `go tool test2json`. Expected discovery was audited against raw events. No Docker command was run by this local verification harness.

| Retained artifact | SHA-256 |
| --- | --- |
| `m4-lifecycle-coordinator-verification/compose-live-final-v2/freeze.json` | `7c9268c7aa4e5822d997270188204cf1edd92f94c09c47f5c3f550fbb5591a31` |
| `m4-lifecycle-coordinator-verification/compose-live-final-v2/receipt.json` | `f1aa4e2a5cfb8fd3f53472cda49a814ce55a0d2cb6aebc0e38a1e6984dfdbe91` |
| Final live fixture | `427ec3a7fbfd7e378a548db06ef0cd74a07fa409bfd990af27740e8c75ac699b` |
| Final pure fixture | `d749f55ebc36d781c5a33ede07e9ec5b683f193b2b0465a2fa88eded4823464d` |
| Final workflow | `6c71562cf039babfcb270fb5cf5ef88d95d4194a98afc0b664249aa17aac0f42` |

Evidence artifacts live under `temp/m3-lan-external-db-20261015/` in the implementation workspace. The first final harness stopped at the formatter check before executing tests. Only receipt-field alignment needed correction; the original source/freeze was preserved. Version 2 records the formatted source and passing run. Earlier pre-correction tests are supplemental, not final-source acceptance. Independent source QA and security review passed; final integration review checks the formatting-only delta and evidence before publication. Documentation dependencies were installed with `pnpm --dir docs install --frozen-lockfile --offline`; the build result is recorded with publication evidence.

The local result covers pure contracts and live-tag compilation/vetting. Actual Compose execution, jq predicates, image availability and Docker cleanup require the hosted job. A local compilation result is not Docker acceptance. The workflow records actual checkout commit/tree separately from event PR head/base because Actions may use a synthetic merge checkout.

## Running and rollback

### Hosted result and Windows fixture correction, 2026-10-20

At draft head `7873f7e895658b790afaf11710cbb15fb11b3b08`, the
[ordinary Compose Docker job](https://github.com/tyhuang9/rig/actions/runs/39176960805/job/117821490478)
passed all four journey cases and the three pure contracts. Its actual checkout
was `bdb2ee54e58b2a72496e90df1864708d582cab6a`, with the same tree as the draft
head (`cad0c606dfaefb3c7757ed6bd174cb02925ed940`). The negative cases intentionally
fail their controller jobs; their passing tests establish the expected refusal
and retention behavior. Exact ownership cleanup and the zero-resource census
passed. Retained log SHA-256:
`742b6a36564e7a8c5cd304232a8dfe4c65afbe846cc3cc7c47363e90214499e4`.

The separate Windows core job failed the pure ownership-manifest test. Windows
runner temporary paths can use short aliases, while the strict writer requires
a canonical directory. The test now resolves its own temporary directory before
calling that writer. Writer validation, create-only writes, and symlink rejection
are unchanged. The failure log is retained with SHA-256
`1b72b75845f58925528f219c5813bf7af502bd24f67825e6774568d9bc2a8263`.

The corrected pure contracts passed on Windows with Go 1.26.7:
`go test -json -count=1 -timeout=2m ./cmd/hostd -run '^TestHostdComposeLive(ObserverClassification|EvidenceRedaction|OwnershipManifest)$'`.
Exactly three parents ran and passed without skips. Log SHA-256:
`5cddd53c7bc436d2487406bc8af54318d166f3243cde3d5208a20190fdca8817`.
The same three parents passed under native Linux using a cross-compiled binary
and `go tool test2json`; log SHA-256:
`53d89b4268504707823f48e7b7a89e90374087da964bca90c1541f2576faedb8`.
Independent source review passed. Hosted Windows verification of the correction
is still required. Two unrelated repository-race packages also exhausted their
40-minute package budgets during fresh database migration setup; that repair is
separate. This is not an all-CI or full-lifecycle acceptance claim.

The dedicated workflow is `.github/workflows/hosting-compose-apply-ci.yml`. Its Go toolchain is pinned to 1.26.7 with readonly modules and CGO disabled. Reproduction requires a disposable Linux Docker host and the preflight described above; do not run against an active controller's data or runtime. The workflow itself specifies setup, exact test selectors, assertions and cleanup.

Before the first hosted pass, this branch remains an acceptance-gate candidate. Retain failed receipts and exact checkout provenance when repairing it. Disabling the new workflow removes this acceptance signal without changing production behavior. Any cleanup after interrupted execution must still verify the manifest and exact ownership; uncertainty means retain resources.

This does not complete M4, full Compose ownership/restart lifecycle or later milestones. It adds no managed database or Neon provisioning. External databases remain application-owned and configured through scoped runtime secrets.
