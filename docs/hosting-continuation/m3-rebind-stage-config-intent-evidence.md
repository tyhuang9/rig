# M3 rebind stage-config intent evidence

Date: 2026-10-03

Base: `d54caa8` (`feature/hosting-m3-rebind-stopped-stage-container-docker-gate`)

Branch: `feature/hosting-m3-rebind-stage-config-intent` (local and unpublished)

Code commit: `57a80f8` (`feat(hosting): pin rebind stage config intent`)

## Scope and invariant

This slice prepares a deterministic successor Caddy stage configuration and
records its exact content and destination in protected sequence seven. The
configuration exposes only the approved successor-address probe and 404
responses for the selected ports; it does not proxy application traffic.
Sequence seven binds the stopped stage container, config volume, protected
successor intent, and predecessor digest already proved in sequence six.

Before appending the intent, the private writer must hold the deployment
effects lease and gateway locks, reattest the prepared claim and all existing
protected and physical predecessor/successor state, and obtain a successful
bounded inventory proving the stopped container's `/config` directory empty.
An error or uncertain archive is never treated as proof of emptiness.
The writer has no Docker write, container start, route change, SQLite
transition, public caller, or application database provisioning effect.
Earlier protected records and their bytes remain immutable; replay may only
accept the exact recorded intent.
Sequence seven is a v1 config-byte format: scanning regenerates its content
digest from the current code. Its empty-route Caddy JSON is pinned by a fixed
input golden length and SHA-256 test. A future config format must keep v1
generation for existing records and introduce an explicit version before
writing new records. A second fixed-input golden pins the v1 probe-token
context and field projection used to build those bytes.

## Verification

| Check | Result |
| --- | --- |
| Focused constructor, scanner, writer, and archive tests | `go test -count=1 -timeout=20m ./internal/generatedingress -run '^(TestGatewayRebindStageConfigV1Bytes\|TestGatewayRebindProgressSequenceSeven\|TestGatewayRebindProgressHistoryRejectsForgedSequenceSeven\|TestGatewayRebindProgressSequenceSevenRejects\|TestGatewayRebindStageConfig\|TestGatewayRebindExactEmpty\|TestGatewayRebindStageContainerCreatesBindsAndReplays)'` passed in 79.813s, before the pure v1 probe-token helper was added. The final-source byte and probe-token tests passed with `go test ./internal/generatedingress -run '^(TestGatewayRebindStageConfigV1Bytes\|TestGatewayRebindStageConfigProbeTokenV1\|TestGatewayRebindStageConfigIsDeterministicProbeAnd404Only)$' -count=1`. |
| Reported concurrent test failure | A reviewer running a broad subset concurrently saw `route_reconciliation_required`, including in unchanged sequence-four cases. The sequence-seven append/replay and sequence-four stale-authorization tests each passed when rerun alone. The final full suite also passed alone, so no reproducible regression remains from this signal. |
| Full Go suite | Final-source solitary `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` completed in 365.610s. The earlier pre-golden run also passed. |
| Static checks | `go vet ./...`, `go build -buildvcs=false ./...`, `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress`, `gofmt -l` on changed Go files, and `git diff --check d54caa8` passed. |
| Web tests | `pnpm --dir web test` could not start because this worktree lacked installed packages and registry fetches returned `EACCES`; the retrying install was stopped. No web source changed in this slice. |
| Live Linux Docker inventory behavior | Not run locally; pending a separate hosted gate after publication authorization. The exact `docker container cp` archive shape is unverified on Linux. |

Manual code, QA, security, and final integration reviews found no remaining
source blocker after the TAR truncation fix, added drift cases, and fixed v1
byte/probe-token goldens. These reviews are separate from executable Docker
acceptance.

## Remaining gates

Sequence seven does not copy configuration. A separate guarded copy and exact
readback receipt, then a hosted Docker test, are required before a stage start.
Stage serving, cutover, terminal receipts, transfer-aware SQLite transitions,
recovery, fence release, public caller, Linux race proof, and physical
second-device LAN acceptance remain open. No hosted acceptance or deployment
is claimed by this local branch.

## Pinned-image directory correction (2026-10-05)

The earlier paragraphs retain the original local checkpoint. This branch is
now draft [PR #129](https://github.com/tyhuang9/rig/pull/129). The correction
starts from clean head `7b95a75f93438b3b6ea3f72e974dd963462a8c51` and changes only
archive admission and its regressions. It does not change sequence-seven JSON,
probe-token generation, protected history bytes, Docker mounts or effects.

The downstream [stage-config-copy job](https://github.com/tyhuang9/rig/actions/runs/37359439273/job/111930044487)
at `f6966a1512bbfa40468567655bb1fd9a1b3197e7` failed both live cases before
sequence seven: direct copy in 71.67s and lost-acknowledgment adoption in 66.43s.
Its cleanup passed. The reported error omitted the underlying parser category;
the actual Docker TAR root and headers were not retained. No physical acceptance
is inferred from this investigation.

Anonymous read-only Docker Registry v2 inspection established the following
immutable image evidence, without creating or starting containers:

| Artifact | Evidence |
| --- | --- |
| Rig's [pinned image index](https://registry-1.docker.io/v2/library/caddy/manifests/sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648) | `sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648` |
| [Linux/amd64 manifest](https://registry-1.docker.io/v2/library/caddy/manifests/sha256:98eb57d882ccd5213d1688764db10c1ca2c58a1ca3a6717a3411ad798f7a423a) | `sha256:98eb57d882ccd5213d1688764db10c1ca2c58a1ca3a6717a3411ad798f7a423a` |
| [Image configuration](https://registry-1.docker.io/v2/library/caddy/blobs/sha256:af555904a0961945f16bb323a501457b13a4f7e9bde969b145b97da80b38ecbe) | Caddy `v2.11.4`; build history creates `/config/caddy` and `/data/caddy` with mode `01777` |
| [Directory layer](https://registry-1.docker.io/v2/library/caddy/blobs/sha256:ee31d5a470f0e64a85c28c82ad55a5bd6b5359db59197c76b9daba7f36c288e9) | 7,502 compressed bytes; downloaded in memory and SHA-256 verified |
| Verified layer headers | `config/` and `data/`: directories, UID/GID `0`, mode `0755`; `config/caddy/` and `data/caddy/`: directories, UID/GID `0`, mode `01777`, zero size, no link |

[Docker's volume documentation](https://docs.docker.com/engine/storage/volumes/#mounting-a-volume-over-existing-data)
states that mounting an empty volume copies the image directory contents by
default. The stage create uses that default. The verified seed directory is
therefore a supported explanation for the strict empty-inventory failure;
the hosted archive itself remains unobserved. Local Docker inspection could
not run because the daemon pipe was absent.

Admission now permits the existing root-only representation or exactly one
empty `caddy/` or `./caddy/` directory with the verified numeric ownership and
mode. It rejects child files/directories, links, duplicates, other names,
ownership/permission drift, hidden PAX/GNU metadata, incomplete trailers and
nonzero trailing data. Archive size limits and the existing root-name allowlist
remain fixed. No directory is deleted, and no broader inventory is accepted.

The new test first failed on both valid seed-directory forms before the fix
(0.638s). An earlier test-only attempt exposed a FIFO fixture encoding error;
that fixture was corrected before the recorded red run. Corrected checks used
normal Windows filesystem access and
`GOCACHE=C:/Users/huang/Documents/Projects/Rig/.go-cache-m3`:

```text
go test -mod=readonly -count=1 -timeout=5m ./internal/generatedingress -run '^TestGatewayRebind(StageConfig|ExactEmpty|ProgressSequenceSeven|ProgressHistoryRejectsForgedSequenceSeven)'
go test -mod=readonly -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayRebindExactEmptyConfigVolumeArchive'
go test -mod=readonly ./internal/generatedingress -list '^TestGatewayRebindExactEmptyConfigVolumeArchive'
```

The broader focused group passed in 112.076s. After adding the final test-only
declared-directory-size case, the two parser tests passed in 0.817s; discovery
required both exact names and passed. Both changed Go files passed `gofmt -l`.
An independent parent review found no source blocker, and the explicit-worktree
`git diff --check` passed. A chained Git check in the elevated test invocation
could not locate the checkout and was rerun separately with the exact path.
No full-suite, Linux race or Docker pass is claimed for this correction.

The relocated stage-file parser in PR #130 and the later final-file pair
parser require the same explicit seed rule. Hosted direct-copy, adoption and
start acceptance must then verify the actual Docker archive. Publication and
merging remain separate authorization gates.
