# M3 rebind stage-config copy evidence

Date: 2026-10-03

Base: `cbc1378` (`feature/hosting-m3-rebind-stage-config-intent`)

Branch: `feature/hosting-m3-rebind-stage-config-copy` (local and unpublished)

Code commit: `0143b51` (`feat(hosting): guard rebind stage config copy`)

## Scope and invariant

This slice guards the Docker write of the sequence-seven probe-and-404-only
Caddy configuration to the bound, stopped successor container. It may copy
only after exact protected, SQLite, predecessor, image, Docker, and host
reattestation under the deployment-effects lease and gateway lock, and strict
proof that `/config` is empty. It must read back byte-for-byte and append one
create-only sequence-eight protected receipt. If an earlier copy succeeded but
its receipt was uncertain, a fresh guarded invocation may adopt only the exact
content. Unexpected content or unreadable inventory remains fenced and is
never overwritten.

The operation does not start a container, publish a listener or route, write
SQLite, expose a public caller, or provision an application database. Protected
sequences one through seven remain byte-for-byte immutable.

## Verification

| Check | Result |
| --- | --- |
| Focused writer, scanner, archive, and failure tests | `go test ./internal/generatedingress -run '^TestGatewayRebindStageConfigCopy' -count=1 -v` passed before QA additions. A broader focused rebind and guarded-copy regression selection passed after QA fixes in 148.992s; archive/driver tests passed in 1.266s and sequence-eight constructor/forgery tests in 6.969s. |
| Full Go suite | Final-source solitary `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` completed in 421.833s. |
| Static and compile checks | `go vet ./...`, `go build -buildvcs=false ./...`, and `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed. Changed Go files passed `gofmt -l`; `git diff --check cbc1378` passed. |
| Web tests | Not rerun for this backend-only slice. The parent branch's `pnpm --dir web test` could not start because missing packages required registry downloads that returned `EACCES`; no web source changed here. |
| Live Linux Docker copy and readback | Not run locally; pending a separate hosted gate after publication authorization. The actual `docker cp` archive shape, stopped-volume write/readback, and physical cleanup remain unverified. |

QA rechecked the final source and found its wrong-byte driver issue fixed, with
the requested replay, retry, TAR, and stale-constructor cases present. Security
review found no material source vulnerability. The aggregate diff received
manual inspection; CodeRabbit was unavailable because the CLI was not
installed in WSL. Final integration review found no source blocker for local
branch review; that verdict does not establish hosted Docker acceptance.

## Remaining gates

Sequence eight ends with a stopped container. Hosted Docker must still prove
the physical copy and inventory format before a start/serving step. Stage
serving, cutover, terminal receipts, transfer-aware SQLite transitions,
recovery, fence release, public caller, Linux race proof, and physical
second-device LAN acceptance remain open. No hosted acceptance or deployment
is claimed by this local branch.

## Pinned-image inventory correction (2026-10-05)

The preceding sections retain the original local checkpoint. This branch is
now draft [PR #130](https://github.com/tyhuang9/rig/pull/130). The correction
starts at clean head `b3d8621831370b3f6bd9d807e7831b6a4123ab1e` and additively
integrates PR #129's `f2614d73e8a6592f0c5196c2daab3e8591a6ad5f`. The one merge
conflict is the earlier relocation of the empty parser into the shared stage
inventory parser. Resolution keeps that delegation and reuses the exact
pinned-image directory predicate; it does not restore a second parser or
rewrite either branch's history.

The [PR #129 evidence](./m3-rebind-stage-config-intent-evidence.md#pinned-image-directory-correction-2026-10-05)
records the failed downstream hosted run and immutable image inspection.
Rig's pinned index `sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648`
resolves on Linux/amd64 to `sha256:98eb57d882ccd5213d1688764db10c1ca2c58a1ca3a6717a3411ad798f7a423a`.
Its [verified directory layer](https://registry-1.docker.io/v2/library/caddy/blobs/sha256:ee31d5a470f0e64a85c28c82ad55a5bd6b5359db59197c76b9daba7f36c288e9)
is 7,502 compressed bytes and contains `config/caddy/`: a zero-length directory,
UID/GID `0`, mode `01777`, with no link. The layer was read in memory and its
SHA-256 verified. Docker's default empty-volume population explains why the
stopped stage can contain that directory before any Rig configuration write.
The failed job did not retain its actual TAR headers; the archive root remains
unverified, and the local Docker daemon was unavailable.

The shared parser now recognizes at most one exact empty image directory,
before or after the single exact `stage.json`. Existing root-only and
directory-absent inventories remain valid. It continues to reject nested
content, duplicate seed/config entries, links, other directories/files,
ownership or mode changes, PAX/GNU metadata, mismatched config bytes, nonzero
padding, truncated terminators and trailing data. The root allowlist and size
limits remain fixed. This changes no config/probe-token bytes, protected
receipts, Docker mounts, resource ownership, effects or uncertain-write policy.

The new stage parser regression failed before adaptation in 0.751s on the
four supported seeded archive forms. The combined empty/stage archive group
passed after adaptation in 0.796s. Final correction checks:

| Check | Result |
| --- | --- |
| Archive success and adversarial cases | `go test -mod=readonly -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayRebindExact(Empty\|Stage)ConfigVolumeArchive'` passed in 0.796s. Required discovery found all four empty/stage archive top-level tests. |
| Guarded intent/copy and immutable history regressions | `go test -mod=readonly -count=1 -timeout=8m ./internal/generatedingress -run '^TestGatewayRebind(StageConfig\|ExactEmpty\|ExactStageConfig\|ProgressSequenceSeven\|ProgressSequenceEight\|ProgressHistoryRejectsForgedSequence(Seven\|Eight))'` passed in 183.118s. |
| Static check | `go vet -mod=readonly ./internal/generatedingress` passed. |
| Documentation | `pnpm --dir docs install --frozen-lockfile --offline`, `pnpm --dir docs check:workflow`, and `pnpm --dir docs build` passed; VitePress built in 2.94s, and the final content rebuild passed in 2.99s with normal Windows access. The sandboxed final rebuild could not resolve the existing pnpm Vite link (`ERR_MODULE_NOT_FOUND`). |

The parent independently reviewed the aggregate parser and test diff and found
no blocker. Changed Go files passed `gofmt -l`, and `git diff --check` passed.
The inherited intent test required line-ending normalization only; this did
not alter its semantics. These checks use the workspace Go cache with normal
Windows filesystem access, as required by existing protected-path fixtures.

The later final-config pair parser must use the same strict seed rule, and
hosted Docker must still establish the actual copy/readback/archive behavior.
No Linux race, full-suite or physical Docker pass is claimed for this correction.
