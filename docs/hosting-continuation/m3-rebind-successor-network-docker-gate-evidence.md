# M3 rebind successor-network Docker gate

Date: 2026-10-03

Base: `542ce48` (`feature/hosting-m3-rebind-guarded-nonterminal`)

Branch: `feature/hosting-m3-rebind-network-docker-gate` (local and unpublished)

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
| Hosted Linux Docker live test and cleanup gate | Not run locally; the local Docker daemon is unavailable. Requires a published draft PR and hosted CI. |
| Physical second-device LAN/address-change proof | Not run; outside this gate. |

The focused fake stage suite failed inside the restricted Windows sandbox
before its fake Docker driver was called, both at the clean base commit and on
this branch. The same test command passed with normal filesystem access. The
failure is consistent with the restricted local lock/file access environment;
it reproduces without this branch's test-only changes.

## Remaining work

The config and data volumes, successor container, serving transition, route
publication, terminal protected receipt, SQLite terminal transition,
migration-035 fence release, public caller, hosted Linux race proof, physical
second-device LAN proof, PR publication, merge, and deployment remain open. The
create-before-bind crash window remains unresolved and fenced; this gate does
not claim automatic recovery from that window.
