# M3 successor final-config intent

Status: local implementation and full verification complete. Unpublished;
hosted race and physical Docker acceptance remain separate requirements.

Base: `98a716d`, `feature/hosting-m3-rebind-stage-start-docker-gate`.
Branch: `feature/hosting-m3-rebind-final-config-intent`.
Implementation: `020196dc0d1250de315713a97f1206bfce7ba2ce`.

## Purpose

Prepare the complete successor application configuration from the retained
predecessor and approved rebind roster, then record a create-only config
intent after fresh sequence-ten attestation. Sequence ten currently proves
challenge/404 service only. This slice supplies the application configuration
needed by later guarded copy and cutover work.

## Required invariants

- Include every predecessor application, including applications without LAN
  grants. Preserve immutable source allocation, grant, profile and digest
  fields; represent the approved successor profile separately.
- Generate deterministic application config with the existing renderer and
  bind its exact bytes, destination, complete route map, source bindings,
  successor identity and preceding protected receipt.
- Reuse the deployment-effects lease, Manager mutex and gateway OS lock in
  their established order. Repeat fresh SQLite, protected-history, Docker,
  config and host proof around the create-only intent and read-back.
- Preserve sequence-seven v1 regeneration and all sequence-one-through-ten
  history bytes and validation semantics. Reject partial, changed, ambiguous
  or unexpected history and config inventory.
- Keep the prepared claim and runtime fences active. The intent writer must
  not copy or activate config, mutate predecessor resources, send application
  requests, publish a route, advance SQLite, expose a public caller, or
  release a fence.

The route plan's `SourceBindingDigest` binds the route, retained LAN binding
and current runtime head together. It is not a SQL transfer or raw-grant
digest. A later transfer writer must use the digest contract defined for its
own ledger rather than substitute this route-plan digest.

## Verification plan and actual evidence

| Check | Expected evidence | Current result |
| --- | --- | --- |
| Baseline | Clean parent full Go suite, vet and build with readonly modules | PASS at `98a716d`: `go test -mod=readonly -p=1 -count=1 -timeout=20m ./...` passed every package (`internal/generatedingress` 700.112s), followed by `go vet -mod=readonly ./...` and `go build -mod=readonly -buildvcs=false ./...`. Run in the unchanged parent worktree with normal filesystem access. |
| Route plan and deterministic config | Complete mixed LAN/loopback roster, exact source identity preservation, stable bytes and effective successor binding | PASS in focused tests: complete map, raw predecessor bindings, deterministic bytes, semantic listener/route assertions and supported 32-character release IDs. |
| Rejection paths | Omitted/extra apps, roster or runtime-head mismatch, changed grants, duplicate/out-of-range ports, wrong generation, history gaps, predecessor/host/Docker drift | PASS in focused tests, including same-app crossed release/slot identities, corruption without partial reads, a forged omitted-loopback plan, and actual loopback SQLite head drift at the coordinator checkpoint. |
| Protected write and replay | Exact create-only sequence-eleven intent, immutable prior bytes, fresh-Manager replay, read-back and ambiguous-write failure | PASS: first ten files remain byte-identical; exact append/replay succeeds; invalid plan writes no file; injected protected-history forgery is rejected; ambiguous write remains an error until fresh replay; earlier-stage entrypoint rejects sequence eleven. |
| Effect audit | No Docker copy/start/reload, application probe, SQL transition, route publication or fence release | PASS in source review and fake-driver tests: observation-only driver, unchanged SQLite/route snapshots, and no extra Docker start/stop. Physical serving proof uses the exact retained sequence-ten stage after full sequence-eleven validation. |
| Final checks | Focused tests, full Go suite, vet, build, live-tag compilation, docs build, formatting and aggregate diff review | PASS on `020196d`: focused appaccess 4.506s and generatedingress 56.136s; discovery found all 16 new top-level tests. Full Go suite passed every package (`generatedingress` 788.957s; appaccess 33.686s), followed by vet, full build and Docker-tag compilation. `gofmt -l` and aggregate diff checks passed. Exact commands are below. |
| Specialist review | Security, QA and final integration review of the completed slice | Independent QA/security and final-integrator source review found no remaining blocker after fixes. Full local checks passed on the reviewed source. CodeRabbit was unavailable as recorded below. |
| Documentation checkpoint | Existing locked dependencies and evidence pages build | PASS: `pnpm --dir docs install --frozen-lockfile --offline` reused all 129 packages; updated evidence passed `pnpm --dir docs build` (3.02s). |
| Hosted and physical proof | Earlier stage Docker gates, Linux race checks, eventual cutover and second-device acceptance | Not established by this local slice. |

Windows Go tests require normal filesystem access because the filesystem
sandbox changes protected-path observations. Local Go has race detection
disabled; a local unit pass is not Linux race or physical Docker acceptance.

## Review checkpoints

Independent runtime-head QA initially reproduced three corrupt states that
the first reader silently omitted: a negative generation, generation zero
with a serving identity, and a head without its application. The reader now
examines those rows and rejects them before returning a snapshot. The five
initial reader tests passed in 4.419s and the full `internal/appaccess`
package passed in 41.528s. Additional deployment/release/slot consistency
regressions then passed in the final focused appaccess run (4.506s); those
earlier results alone did not establish their acceptance.

Review also identified supported 32-character lowercase hexadecimal release
IDs, a recomputed plan that omits a loopback-only application, and rejection
before an invalid create-only write as explicit regression requirements.
All three are implemented and covered by the final focused run.

The new mixed-app fixture exposed nondeterministic synthetic Docker network
IDs in a shared test helper: unsorted map iteration could assign different
IDs to the same networks between observations. Sorting network names fixed
the fixture; production freshness checks remain strict. The checkpoint test
uses the real sequence-eight, nine and ten coordinators to construct its
serving state. Its separate focused run passed in 9.317s.

Exact focused commands used readonly modules and normal filesystem access:

```powershell
go test -mod=readonly -p=1 -count=1 -timeout=10m -run '^TestGatewayRebindRuntimeHeads' ./internal/appaccess
go test -mod=readonly -p=1 -count=1 -timeout=10m -run '^(TestGatewayRebindFinalConfig|TestGatewayRebindStageStartStill)' ./internal/generatedingress
```

The following final commands ran sequentially on the frozen implementation;
every command exited successfully. The last command compiles tagged tests
without executing physical Docker journeys.

```powershell
go test -mod=readonly -p=1 -count=1 -timeout=20m ./...
go vet -mod=readonly ./...
go build -mod=readonly -buildvcs=false ./...
go test -mod=readonly -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress
```

CodeRabbit CLI 0.8.2 was found in WSL's login environment. Its
`coderabbit auth status --agent` check returned `not_authenticated` on
2026-10-05, so no CodeRabbit review ran and no login flow was initiated.
Independent QA/security review is recorded separately from CodeRabbit.

## Inherited acceptance

The row-25 ancestor `9ad7f78` passed its hosted private network-stage test and
cleanup, all three dedicated generated-runtime race batches, and the stable
aggregate check. The ingress batches ran 455 Linux tests: 448 passed and
seven existing environment-gated tests skipped. The separate repository-wide
race job also passed, completing at 2026-10-05 17:34:51 UTC. Its PostgreSQL
checks passed and `internal/generatedingress` passed in 2291.490s. Exact links
and counts are retained in the successor-network Docker-gate evidence. These ancestor results do not
establish sequence-eleven or later-stage acceptance.

## Remaining work and rollback

Later work must copy and attest the inactive final config, prepare and switch
the exact final container, prove predecessor shutdown, retain terminal
receipts, transition SQLite through guarded writers, implement transfer-aware
readers and recovery, and establish physical LAN acceptance. No publication,
merge or deployment is authorized by local implementation. Preserve all
protected history and leave uncertain operations fenced.

The next private copy slice must distinguish an exact stage-only inventory
from the exact stage-plus-active pair. Sequence eleven is already the
pre-effect intent; a later sequence-twelve receipt should follow verified
copy/readback. After a lost copy acknowledgment, only that new coordinator
may adopt the exact pair under fresh proof. Existing sequence-eleven replay
must continue rejecting an unexpected active file. The bounded two-file TAR
reader must accommodate both configuration files and framing without raising
generic command-output limits. No reload, listener change, application
request, SQL transition or fence release belongs in that copy slice.

## Prerequisite archive integration (2026-10-05)

The stage-config Docker job on this draft's previous head
`f6966a1512bbfa40468567655bb1fd9a1b3197e7` failed before configuration copy:
[hosted job 111930044487](https://github.com/tyhuang9/rig/actions/runs/37359439273/job/111930044487).
The always-run cleanup passed. Immutable pinned-image inspection established
an empty root-owned `caddy/` directory with mode `01777` in `/config`.
The [stage intent evidence](./m3-rebind-stage-config-intent-evidence.md#pinned-image-directory-correction-2026-10-05)
records the image digests and strict compatibility correction.

PRs #129 through #135 now retain the original histories and additively integrate
that correction. This branch's tested source is
`9fd7cdbf4439f369613e6191153b281bbe32bee2`. PR #131 also adds bounded archive-header
diagnostics on failure; it prints fixed categories and numeric metadata, never
configuration bytes or arbitrary paths. The parser accepts at most one exact
empty image directory. Other entries, ownership changes, nested content,
metadata, duplicates, malformed framing and unexpected configuration remain
rejected. No Docker ownership, permissions, protected bytes or effects changed.

Verification used normal Windows access and the shared workspace Go cache:

```text
go test -mod=readonly -p=1 -json -count=1 -timeout=15m -run '^TestGatewayRebind(Exact(Empty|Stage)ConfigVolumeArchive|StageStart|FinalConfig)' ./internal/generatedingress
go vet -mod=readonly ./internal/generatedingress
go build -mod=readonly -buildvcs=false ./...
go test -mod=readonly -p=1 -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress
```

All 36 explicitly discovered top-level regression tests passed in 285.669s;
JSON inspection found no failures or skips. Vet, build and tagged compilation
passed. PR #130's guarded intent/copy/history tests also passed in 183.118s,
with archive tests in 0.796s and documentation checks/build passing.
The parent reviewed the aggregate diff and preserved each draft's base.

Actual Docker TAR headers and corrected hosted acceptance remain pending.
The local Docker engine is unavailable. No full-suite, Linux race or physical
Docker pass is claimed for this correction. Updating these authorized drafts
does not authorize merging or deployment.
