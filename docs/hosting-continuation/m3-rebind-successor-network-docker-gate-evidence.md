# M3 rebind successor-network Docker gate

Date: 2026-10-03

Original base: `542ce48` (`feature/hosting-m3-rebind-guarded-nonterminal`).
The current local gate also contains the additive row-24 route correction.

Branch: `feature/hosting-m3-rebind-network-docker-gate`, published as draft
[PR #122](https://github.com/tyhuang9/rig/pull/122).

## Scope

This branch adds a hosted Linux Docker acceptance gate for the private first
successor effect introduced by the base branch. The test uses the production
Docker and host-network readers against a disposable local Docker daemon. It
prepares an actual predecessor gateway and committed LAN grant, then stages a
generation-scoped successor ingress network. It checks the exact Docker network
ID and protected sequence-three binding, private bridge shape, one exact
deterministic host bridge candidate, the exact hosted Linux subnet, gateway
`/32`, and broadcast `/32` route delta and planned interface prefix,
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
| `go test -list '^TestLiveGatewayRebindSuccessorNetworkStage$' ./internal/generatedingress` | PASS; exact test discovered. |
| `go test -run '^TestLiveGatewayRebindSuccessorNetworkStage$' -count=1 -v ./internal/generatedingress` | SKIP on Windows because `RIG_RUN_LIVE_GATEWAY_V2` is unset. The hosted job sets it and rejects a skip. |
| `go test -count=1 -timeout=5m -run '^TestGatewayRebindStageNetwork' ./internal/generatedingress` | PASS, 25.812s on the original Docker-gate source. The corrected matcher and post-create no-binding regression passed in the row-24 focused run (34.596s) and in the final integrated full suite below. |
| `go test -p=1 -count=1 -timeout=20m ./...` | PASS on the integrated route-correction and live-gate source; generated-ingress completed in 195.781s. |
| `go vet -tags live_docker ./internal/generatedingress` | PASS on the original Docker-gate source. |
| `go vet ./...` | PASS on the integrated source. |
| `pnpm --dir web install --frozen-lockfile --prefer-offline --fetch-retries=0` | PASS with the exact lockfile after the restricted-network attempt failed. |
| `pnpm --dir web test` | PASS; 19 files and 502 tests. |
| `pnpm --dir web build` | PASS; TypeScript and Vite production build. Vite reported its existing large-chunk advisory. |
| `go build -buildvcs=false ./...` | PASS on the integrated source. Plain `go build ./...` could not obtain VCS status from this local worktree (exit 128), before compilation. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation on the integrated source; no physical Docker test executed. |
| `pnpm --dir docs build`, `gofmt`, and `git diff --check` | PASS on the integrated source; no format or whitespace defects. |
| Hosted Linux Docker live test and cleanup gate | PASS at code head `ad8d3e2`: the named test emitted a pass event in 54.89s and the always-run residue step succeeded in [job 111606812391](https://github.com/tyhuang9/rig/actions/runs/37260595415/job/111606812391). Four earlier attempts failed; see below. The local Docker daemon is unavailable. |
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

The published row-21 and row-24 corrections normalize only the order
of the independently validated exact predecessor Docker mounts in transient
observation digests; they preserve all other drift checks and protected record
bytes. Commit `cccbee3` covers the full pre-create path: alternating exact
mount order across eight predecessor reads creates one network and binds
sequence three, while wrong mounts and non-mount drift fail before create.
The focused stage-network group passed in 39.386s. The final-source serialized
uncached Go suite passed with every package (`internal/generatedingress`
219.779s). `go vet ./...` and
`go build -buildvcs=false ./...` passed at the local row-25 tip. Security
review found no ownership or history bypass. The corrected head ran on hosted
Docker and failed again; the mount-order correction was insufficient. Docker-tagged compilation, web typecheck, all 502 web tests,
web production build, and docs build passed on the earlier row-25 source before
the Go-only digest correction. Those local checks do not establish the hosted
network effect.

The [corrected-head hosted job](https://github.com/tyhuang9/rig/actions/runs/37254601656/job/111588887508)
failed on 2026-10-05 at progress count two, before sequence-three binding.
The Docker network existed and passed exact identity inspection, the private
bridge candidate matched, and predecessor Docker observations were valid and
equal both semantically and by raw digest. The host read succeeded, but the
route delta was not exact; interface, Docker-ID, and Docker-prefix deltas were
exact, while the separate physical route read failed. The test retained the
unbound network and the always-run residue gate failed as intended. The exact
route discrepancy remained under investigation at that run. This is failed
acceptance evidence, not a reason to relax the binding or cleanup checks.

The [fourth hosted diagnostic job](https://github.com/tyhuang9/rig/actions/runs/37257206284/job/111596748764)
failed before sequence-three binding and retained the unbound network. Its
failure-only multiset comparison measured 22 baseline host routes and 25 after
network creation: exactly one planned subnet route, one local gateway `/32`,
and one broadcast `/32`. It found zero removed baseline routes, other routes,
or duplicate additions. The residue gate failed as designed. This explains
why the previous baseline-or-subnet-only matcher rejected the otherwise exact
Docker bridge. The row-24 correction admits only the complete
three-route Linux delta; the row-25 live assertion is updated to require that
same exact physical shape. Focused local tests cover complete, missing,
duplicate, and unrelated route changes, including failure before protected
binding. The integrated source passed the serialized full Go suite, vet, Go
build, Docker-tagged compilation, and docs build. Security, QA, and final
integration review found no new binding or cleanup bypass. Hosted acceptance
on the corrected pair was established by the fifth hosted run below. This
proves the tested network-stage and replay path on that runner, not later
successor effects or physical LAN cutover.

## Fifth hosted attempt: network-stage acceptance, 2026-10-05 UTC

At immutable code head `ad8d3e2`, [hosted run 37260595415 and network-stage
job 111606812391](https://github.com/tyhuang9/rig/actions/runs/37260595415/job/111606812391)
completed successfully. The required `TestLiveGatewayRebindSuccessorNetworkStage`
emitted a JSON test `pass` event, with no skip, after 54.89 seconds. The
workflow's always-run `Require complete rebind network cleanup` step also
passed, finding no managed containers, networks, volumes, generated images, or
named successor network residue. The prerequisite gateway-v2 Docker job passed
in the same workflow. The test's production-reader assertions cover exact
Docker network identity and bridge/IPAM, exact Linux host-route delta,
protected sequence-three binding, unchanged predecessor state and SQLite
claim, and replay without another create. This is acceptance evidence for the
private successor-network stage only. The four earlier failed jobs remain
recorded above as immutable failure history.

The broader generated-runtime race check is still a separate PR-readiness
gate. At evidence-only head `f91140c`, [job 111608365118](https://github.com/tyhuang9/rig/actions/runs/37261058991/job/111608365118)
timed out at the inherited 18-minute Go package limit, without a race report.
The previous successful pre-correction generated-ingress race package had
completed in 1074.037s, only 5.963s below that limit. The parent row-24 branch
now raises the Go, step, and job budgets to 24, 26, and 35 minutes without
removing test coverage. This correction has not yet passed hosted race CI.

## Remaining work

The config and data volumes, successor container, serving transition, route
publication, terminal protected receipt, SQLite terminal transition,
migration-035 fence release, public caller, hosted Linux race proof, physical
second-device LAN proof, merge, and deployment remain open. The
create-before-bind crash window remains unresolved and fenced; this gate does
not claim automatic recovery from that window.
