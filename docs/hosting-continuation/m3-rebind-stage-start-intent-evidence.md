# M3 rebind stage-start intent evidence

Date: 2026-10-04

Base: `a9e2b54` (`feature/hosting-m3-rebind-stage-config-docker-gate`)

Branch: `feature/hosting-m3-rebind-stage-start-intent` (local and unpublished)

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
