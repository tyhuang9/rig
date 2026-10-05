# M3 rebind guarded stage start evidence

Date: 2026-10-04

Base: `dae04db` (`feature/hosting-m3-rebind-stage-start-intent`)

Branch: `feature/hosting-m3-rebind-stage-start` (local and unpublished)

Scope commit: `b455d42`

## Scope and invariants

The sequence-ten slice may start only the exact sequence-nine bound successor
container. It must reattest the protected claim, predecessor, image, Docker and
host topology, selected interface, stopped successor, and exact copied config
under the deployment-effects lease and gateway lock immediately before the
start command. It must not treat a prior free-port observation as permission.

A receipt may be appended only after fresh physical proof of the exact running
container, sole network attachment and endpoint, effective selected-address
port bindings, live and restart config, and rebind-specific per-port challenge
responses from both host and container. Selected and wrong Host roots must
return the expected 404, and loopback must remain unavailable. A sequence-nine
running stage may be adopted after a lost start acknowledgement only if it
passes the same complete proof; it must not be started again. A sequence-ten
replay must reprove serving without a start.

If serving proof fails after an attempted start, any compensating stop must
target only the fully reattested successor ID and prove withdrawal. An
uncertain stop or live state must retain the fence and report that the
candidate may be live. An ambiguous protected receipt installation ends the
invocation; fresh replay must resolve it. The predecessor, route, application
URL, SQLite claim, application-owned databases, and earlier protected records
must remain unchanged. This slice does not connect application networks,
cut over traffic, or release the fence.

## Verification plan

| Check | Required evidence | Result |
| --- | --- | --- |
| Focused history and controller tests | Exact seq10 append and scanner, seq1–9 byte/digest compatibility, direct start, lost-ack adoption, fresh replay, drift and compensation, command audit | PASS; focused seq10 suite completed in 189.915s. Checkpoint regressions completed in 31.611s and final proof/validator regressions in 9.388s. |
| Generated-ingress package and review | All generated-ingress tests, vet, format, and security/QA review of the aggregate slice | PASS; `go test ./internal/generatedingress -count=1` completed in 585.531s, `go vet ./internal/generatedingress` passed, and reviewers found no remaining concrete local blocker after the withdrawal and checkpoint repairs. |
| Full local checks | Serialized uncached top-level Go suite, vet, build, Docker-tagged compile, gofmt, aggregate diff and status | PASS on the local code commit: `go test -p=1 -count=1 -timeout=20m ./...` completed with every package passing (`internal/generatedingress` 586.586s); `go vet ./...`, `go build -buildvcs=false ./...`, `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress`, and `gofmt -l` passed. Final ancestor integration remains pending. |
| Documentation build | `pnpm --dir docs build` | PASS after merging the published row-3 ancestor fix `dd005c3` at local merge `3a72609`; VitePress built and rendered all pages. The earlier pre-merge run failed on its unescaped Docker server-version template expression and is recorded as an inherited failure, not a sequence-ten regression. |
| Hosted Docker proof | Direct start, fresh-Manager lost-ack adoption, proof-failure compensation, exact residue cleanup, predecessor/request-count/route/SQLite invariants | Not run. This requires a separately reviewed Docker gate and publication approval. |
| Physical second-device LAN | Selected-host reachability and loopback isolation on another device | Not run; a later M3 acceptance gate. |

Local Windows Docker is unavailable, so compilation and fakes cannot establish
physical container, host bind, or network behavior. Do not claim hosted
acceptance from this branch's local tests.

The prerequisite row-25 [hosted network-stage attempt](https://github.com/tyhuang9/rig/actions/runs/37242877446/job/111554907291)
failed after network creation but before a protected identity was bound. Its
diagnostic rerun is queued. Sequence ten remains local and unpublished while
that gate and the inherited stack repairs are resolved.

## Remaining gates

The prepared rebind claim and runtime fence remain active. Public cutover,
terminal protected receipts, transfer-aware SQLite transition, recovery, Linux
race proof, and physical second-device LAN acceptance remain separate M3 work.
Migrations 033 and 034 remain dormant and unreleasable.

## Rollback

Keep this branch unpublished if local review fails. If a later authorized draft
fails hosted proof, leave it unmerged and correct or revert the change. Do not
delete protected history or force-remove an uncertain Docker resource. Any
runtime compensation must be exact-owned and proven; otherwise retain the
fence for explicit recovery.
