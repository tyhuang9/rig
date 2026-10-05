# M3 rebind stage-start intent evidence

Date: 2026-10-04

Base: `a9e2b54` (`feature/hosting-m3-rebind-stage-config-docker-gate`)

Branch: `feature/hosting-m3-rebind-stage-start-intent` (now draft PR #132).
The original scope and verification below describe the local intent-only
checkpoint; the later compatibility correction is recorded separately.

Scope commit: `3dfee6b`

## Scope

Sequence nine records an append-only, protected intent for starting the exact
sequence-eight successor stage container. It binds the already copied config,
stopped container, private network, selected host interface and port range,
container address, and rebind challenge contract to a deterministic start
effect. The writer must reattest the protected and physical predecessor under
the deployment-effects lease and gateway lock before appending the receipt.

This step does not call `docker container start`, expose host listeners, probe
the successor, change the active route or app URL, transition SQLite, modify
an application database, or release the deployment fence. A later guarded
start step must recheck all physical preconditions immediately before the
effect. A negative port-availability observation cannot be durable
authorization because another process can claim a port after observation.

Sequence-nine replay must require the same protected record and still-stopped
physical state. An uncertain protected append ends that invocation; a fresh
invocation may adopt only an exact record and must not start the container.
Earlier sequence-one through sequence-eight records and digests must remain
byte-compatible. Old binaries encountering sequence nine must fail closed.

## Verification

| Check | Result |
| --- | --- |
| Focused sequence-nine tests | Passed: `go test -count=1 -timeout=10m -run '^TestGatewayRebindStageStart(Intent|Effect)' ./internal/generatedingress` (52.095s). This covers exact append/replay, post-receipt drift, four-read config stability, uncertain install, forged history, the v1 effect projection, and pre-start JSON/digest compatibility for sequences one through eight. |
| Full Go suite | Passed on final source: `go test -p=1 -count=1 -timeout=20m ./...`; `internal/generatedingress` passed in 470.298s and every package completed. The run was logged to a temporary file. An earlier run lost its terminal handle without a final result and is not counted. |
| Vet and build | Passed on final source: `go vet ./...`; `go build -buildvcs=false ./...`. |
| Docker-tagged compile | Passed: `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress`; both packages compiled with no live tests run. |
| Formatting and diff | Passed: `gofmt -l` listed no changed Go files; `git diff HEAD --check` reported no whitespace errors. The aggregate code diff and documentation diff were inspected separately before commit. |
| Static safety review | Security review found no blocker under the protected controller-directory trust boundary. Independent manual review caught a nondeterministic test golden; it was removed and replaced with a pre-start schema comparison, and focused tests passed. QA's replay, drift, and projection gaps were addressed. Final integration review found no blocking contradiction and approved a local commit, conditional on future hosted acceptance. The optional local CodeRabbit review was unavailable because its agent session was unauthenticated; its result is not claimed. |
| Hosted Docker acceptance | Not run for this local branch. No Docker start occurs in sequence nine, and local Windows Docker is unavailable. The physical start and serving proof belong to the later sequence-ten hosted gate. |

The command boundary uses read-only Docker inspection and config archive
readback (`container cp <id>:/config/. -`). Static command audit found the
`["container", "start", id]` slice only inside the future-effect digest
projection; the sequence-nine driver exposes no start, copy-write, route, or
SQLite transition operation. The stage remains stopped throughout the exact
append and fresh-Manager replay tests. No web files changed; web tests and
production build were not rerun for this Go-only intent branch.

## Remaining gates

The stage container remains stopped. Sequence ten needs a separate guarded
start and physical serving receipt, including exact bindings, network
endpoint, per-port challenge and wrong-Host 404 proofs, and failure adoption.
Public cutover, terminal receipts, recovery, Linux race proof, and physical
second-device LAN acceptance remain separate M3 gates. Migrations 033 and 034
remain dormant and unreleasable.

## Rollback

Leave this unmerged branch unpublished if local verification fails. If a later
reviewed publication fails, correct or revert this intent-only change; no
Docker start, route, SQLite, or application database rollback is required by
sequence nine itself. Preserve uncertain protected history for diagnosis.

## Pinned Caddy autosave compatibility correction — 2026-10-05

Correction base: `a06ef26847a7a926cb535c05be48c7f0910fbe48` on the existing
PR #132 branch. This is a read-only inventory correction; no generated config,
Docker command, protected record, digest projection, or historical byte format
changes. It adds no start, stop, reload, route, SQLite, or fence-release effect.

### Source evidence and phase boundary

Caddy v2.11.4 loads configuration by decoding it into an untyped JSON value,
marshaling the resulting raw configuration, and persisting that snapshot by
default with mode `0600`. Its configured `XDG_CONFIG_HOME=/config` resolves the
snapshot to `/config/caddy/autosave.json`. The Rig container runs as UID/GID
1000, and the separately verified pinned-image `caddy/` directory is root-owned
mode `01777`. The relevant immutable source references are
[Caddy configuration loading and autosave](https://raw.githubusercontent.com/caddyserver/caddy/v2.11.4/caddy.go),
[raw configuration decoding](https://raw.githubusercontent.com/caddyserver/caddy/v2.11.4/admin.go),
[initial command loading](https://raw.githubusercontent.com/caddyserver/caddy/v2.11.4/cmd/commandfuncs.go),
and [the autosave path](https://raw.githubusercontent.com/caddyserver/caddy/v2.11.4/storage.go).
These establish why a successful start can add a snapshot; they do not establish
the actual Docker archive headers on the hosted runner.

The pre-start parser remains strict and bounded to 64 KiB. The stage-start
driver can read a 128 KiB archive only after a fresh protected-history scan
proves the exact durable sequence-nine binding. It scans again after the
read and requires unchanged history. The snapshot is optional because Caddy
logs a persistence failure without making the successfully loaded service
fail. If present, it must be one exact regular file, UID/GID 1000, mode `0600`,
with exactly the canonical approved stage bytes; the exact stage file and
image-owned seed directory must also be present. Observed JSON is never
normalized into acceptance. Unknown files, nested paths, links, duplicates,
hidden PAX/GNU metadata, nonzero padding, truncation, and trailing data remain
rejected. Expected and canonical config sizes each remain bounded to 60 KiB.

A compensated or uncertain stage start may leave this same snapshot in a
stopped container. The new reader admits that exact snapshot only after the
durable start intent. It preserves the sequence-nine prefix when later phases
append their own fields, while requiring a fresh complete protected-history
scan. A caller-supplied start binding alone does not authorize this path.

### Correction verification

| Check | Result |
| --- | --- |
| Existing strict archive baseline | Passed before edits: `go test -mod=readonly -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayRebindExact(Empty\|Stage)ConfigVolumeArchive'` (0.620s). |
| Meaningful red/green regression | `go test -mod=readonly -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayRebindStageStartInventoryAcceptsExactAutosaveAfterDurableIntent$'` failed before the correction (3.568s, exact post-start snapshot rejected as an invalid archive entry), then passed after it (4.091s). |
| New focused regressions | All five new top-level tests were discovered with `go test -mod=readonly ./internal/generatedingress -list '^TestGatewayRebind(StageAutosave\|StartedStageArchive\|StageStartInventory)'`, with an exact-count assertion. The initial expanded group passed in 9.450s; final broader results are recorded below. |
| Final broader regression group | Passed on final Go source: `go test -mod=readonly -count=1 -timeout=8m ./internal/generatedingress -run '^TestGatewayRebind(StageAutosave\|StartedStageArchive\|StageStartInventory\|StageStart(Intent\|Effect)\|StageConfig\|ExactEmpty\|ExactStageConfig\|ProgressSequence(Seven\|Eight\|Nine)\|ProgressHistoryRejectsForgedSequence(Seven\|Eight\|Nine))'` (275.423s). This includes the corrected payload-padding regression, all five new tests, the existing strict archive parsers, config-copy/intent/volume behavior, sequence-nine effects and immutable prior-record checks. |
| Scoped static checks | `go vet -mod=readonly ./internal/generatedingress` and `go build -mod=readonly -buildvcs=false ./internal/generatedingress` passed. |
| Docker-tagged compile | Passed: `go test -mod=readonly -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` (hostd 0.816s, ingress 0.767s); this compiled the tagged suites without running live effects. |
| Documentation and formatting | Locked offline documentation install passed; `pnpm --dir docs build` passed (3.84s), and `pnpm --dir docs check:workflow` passed. `gofmt -l` listed no changed Go files and `git diff --check` passed. |
| Review | Independent parent review found no source blocker in the parser, canonical helper, or durable-history driver admission. No CodeRabbit result is claimed. |
| Physical acceptance | Not run locally: the Windows Docker Linux engine is unavailable. Actual pinned-image TAR headers, stage-start post-readback, and subsequent final-pair/handover policy remain hosted Docker acceptance gates. |

The full Go suite for a separate frozen handover checkpoint is independent of
this correction and is not claimed as verification of these edits. Downstream
final config inventory requires its own phase-aware policy before Docker
readiness can be claimed. If hosted readback disagrees with the narrow expected
headers or snapshot, preserve the protected history and stop advancement;
do not clear volumes, rewrite prior config, or weaken the exact inventory.
