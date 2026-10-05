# M3 rebind successor-network Docker gate

Date: 2026-10-03

Base: `542ce48` (`feature/hosting-m3-rebind-guarded-nonterminal`)

Branch: `feature/hosting-m3-rebind-network-docker-gate`, published as draft
[PR #122](https://github.com/tyhuang9/rig/pull/122).

## Scope

This branch adds a hosted Linux Docker acceptance gate for the private first
successor effect introduced by the base branch. The test uses the production
Docker and host-network readers against a disposable local Docker daemon. It
prepares an actual predecessor gateway and committed LAN grant, then stages a
generation-scoped successor ingress network. It checks the exact Docker network
ID and protected sequence-three binding, private bridge shape, one exact
deterministic host bridge candidate, the planned route and interface prefix,
and only the bound Docker network ID and planned Docker prefix. It also checks
the unchanged prepared SQLite claim, predecessor resources and route, no extra
application request, and exact replay without another create.

The successor uses an interface and IPv4 address present on the CI host. This
tests the adapter and network-stage invariants; it does not prove a physical
address change, second-device reachability, route cutover, or terminal SQLite
transition. Migration 034 remains fenced.

The test cleans up only a network whose exact ID is bound in protected
progress and whose complete ownership and physical shape still match. An
unbound or uncertain resource is retained and causes the CI residue gate to
fail for investigation. The separate workflow job also rejects a skipped live
test and any remaining managed Docker resources or successor network name.

## Verification

| Check | Result |
| --- | --- |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS; compile only, no live test executed. |
| `go test -list '^TestLiveGatewayRebindSuccessorNetworkStage$' ./internal/generatedingress` | PASS; exact test discovered. |
| `go test -run '^TestLiveGatewayRebindSuccessorNetworkStage$' -count=1 -v ./internal/generatedingress` | SKIP on Windows because `RIG_RUN_LIVE_GATEWAY_V2` is unset. The hosted job sets it and rejects a skip. |
| `go test -count=1 -timeout=5m -run '^TestGatewayRebindStageNetwork' ./internal/generatedingress` | PASS, 25.812s with normal Windows filesystem access. |
| `go test -count=1 -timeout=20m ./...` | PASS on the final source; generated-ingress completed in 173.797s. |
| `go vet -tags live_docker ./internal/generatedingress` | PASS. |
| `go vet ./...` | PASS. |
| `pnpm --dir web install --frozen-lockfile --prefer-offline --fetch-retries=0` | PASS with the exact lockfile after the restricted-network attempt failed. |
| `pnpm --dir web test` | PASS; 19 files and 502 tests. |
| `pnpm --dir web build` | PASS; TypeScript and Vite production build. Vite reported its existing large-chunk advisory. |
| `go build -buildvcs=false ./...` | PASS. Plain `go build ./...` could not obtain VCS status from this local worktree (exit 128), before compilation. |
| `gofmt -l` on both touched Go tests and `git diff --check` | PASS; no format or whitespace defects. |
| Hosted Linux Docker live test and cleanup gate | FAILED on two hosted runs; see the attempts below. The local Docker daemon is unavailable. |
| Physical second-device LAN/address-change proof | Not run; outside this gate. |

The focused fake stage suite failed inside the restricted Windows sandbox
before its fake Docker driver was called, both at the clean base commit and on
this branch. The same test command passed with normal filesystem access. The
failure is consistent with the restricted local lock/file access environment;
it reproduces without this branch's test-only changes.

## Hosted attempt, 2026-10-05 UTC

The first [hosted network-stage job](https://github.com/tyhuang9/rig/actions/runs/37242877446/job/111554907291)
ran the required test without skipping it. Docker created the successor network,
but the stage returned `route_reconciliation_required` before installing the
protected sequence-three identity. The test retained the unbound network, and
the residue gate failed. The job did not reveal which post-create predicate
failed. This is a failed acceptance attempt, not evidence that the network
stage works on hosted Docker.

Commit `9fe6278` adds failure-only predicate diagnostics to the live test. It
does not relax production checks or remove uncertain resources. The
[diagnostic hosted job](https://github.com/tyhuang9/rig/actions/runs/37250455749/job/111576832137)
also failed the named test with `route_reconciliation_required` at progress
count two. At the failure snapshot the successor network was absent, while the
protected anchor, host candidate/routes/interfaces and two predecessor Docker
observations were valid and equal. The residue step found no remaining managed
resources. This snapshot does not establish which earlier pre-effect or
post-effect predicate returned the error, so the network gate remains failed.

The local unpublished row-21 and row-24 corrections normalize only the order
of the independently validated exact predecessor Docker mounts in transient
observation digests; they preserve all other drift checks and protected record
bytes. Focused stage-network tests, the serialized uncached Go suite
(`internal/generatedingress` 198.001s), `go vet ./...`, and
`go build -buildvcs=false ./...` passed at the local row-25 tip. Security
review found no ownership or history bypass. These corrections have not yet
run on hosted Docker and are not claimed as the cause or remedy of either
hosted failure. Docker-tagged compilation, web typecheck, all 502 web tests,
web production build, and docs build passed on the earlier row-25 source before
the Go-only digest correction. Those local checks do not establish the hosted
network effect.

## Remaining work

The config and data volumes, successor container, serving transition, route
publication, terminal protected receipt, SQLite terminal transition,
migration-035 fence release, public caller, hosted Linux race proof, physical
second-device LAN proof, merge, and deployment remain open. The
create-before-bind crash window remains unresolved and fenced; this gate does
not claim automatic recovery from that window.
