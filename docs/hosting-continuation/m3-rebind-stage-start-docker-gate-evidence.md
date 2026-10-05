# M3 rebind guarded stage-start Docker gate evidence

Date: 2026-10-04

Base: `feature/hosting-m3-rebind-stage-start` at `b455d42` when implementation
began. The row-25 Linux route correction and passing network-gate evidence
were merged additively through rows 26–36, then into this branch at `6305bdc`.
The later race-budget and live-test parity corrections, with row-25 acceptance
evidence through `f1707e9`, were also merged additively through rows 26–37.
The measured CI timeout correction at row-25 head `6023a0f` was then merged
through the same local chain.

Branch: `feature/hosting-m3-rebind-stage-start-docker-gate` (local and unpublished)

## Scope

Three named live tests use the default local Linux Docker daemon after preparing
the exact sequence-nine start intent. They require (1) one guarded Docker start,
sequence-ten serving receipt, and observation-only replay from a fresh Manager;
(2) adoption of a real start whose acknowledgment was lost before the
sequence-ten write, without another Docker start; and (3) a deliberately failed
post-start challenge proof followed by an exact Docker stop, two selected-address
and loopback withdrawal probes, and unchanged sequence-nine history. The tests
also compare the prepared SQLite claim, current app access, predecessor route
and its URL inputs, protected predecessor, earlier protected bytes, exact
predecessor Docker state, and routed application request count.

The proof-failure test first calls the real host probe and requires the exact
challenge response to be ready before substituting one synthetic failed probe.
This makes a naturally failing Caddy startup fail the test rather than appear
to prove compensation. The physical fixture binds one port; sequence-ten unit
tests cover multi-port publication and withdrawal behavior. Hosted execution
must still prove the one-port Docker journey.

Cleanup proves generation-scoped protected lineage through sequence nine or ten,
stops only the exact attested running stage, proves listeners withdrawn, then
removes exact stopped-stage resources without force. Ambiguous effects retain
their resources so the always-run CI residue check fails. The workflow requires
an individual JSON pass event and rejects a skip for each named test.

This gate does not change production runtime code, release a fence, transition
SQLite, cut over a route, provision a database, merge, or deploy.

## Verification

| Check | Result |
| --- | --- |
| Live-tag compile | Passed on post-route-integration head `6305bdc`: `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress`; the physical tests did not run. |
| Named test discovery | Passed: `go test -count=1 -run '^TestLiveGatewayRebindSuccessorStageStart(LostAcknowledgmentAdopts|ProofFailureWithdraws)?$' -v ./internal/generatedingress`; all three were discovered and explicitly skipped without the Docker opt-in. This is not physical acceptance. |
| Focused stage-start unit tests | Passed after the QA correction: `go test -p=1 -count=1 -timeout=10m -run '^TestGatewayRebindStageStart' ./internal/generatedingress` (`205.641s`). |
| Workflow YAML | Passed with a temporary checker using the repository's `gopkg.in/yaml.v3` dependency; the `rebind-stage-start-live` job parsed and was present. Temporary checker removed. |
| Physical Docker journeys | Not run locally. Docker CLI is installed, but the Windows host has no running Docker Engine. Hosted Linux Docker acceptance has not run. |
| Prerequisite network-stage hosted gate | PASS at corrected code head `fd68ab8`: [named Docker test and always-run residue job 111619317032](https://github.com/tyhuang9/rig/actions/runs/37264835255/job/111619317032). The later row-25 evidence head `f1707e9` also passed its [named test without a skip and cleanup job 111620115206](https://github.com/tyhuang9/rig/actions/runs/37265095730/job/111620115206) in 46.06s. The current CI-budget head `6023a0f` is rerunning hosted checks. These jobs establish only the private network effect, not this stage-start gate. |
| Generated-ingress package tests | Passed: `go test -p=1 -count=1 -timeout=20m ./internal/generatedingress` (`648.529s`). This run began before a small assertion was added to the live test, which skips locally; final-source live-tag compilation and named-test discovery passed afterward. |
| Vet and build | Passed on post-route-integration head `6305bdc`: `go vet ./...` and `go build -buildvcs=false ./...`. |
| Documentation build | Passed on post-route-integration source: `pnpm --dir docs build` with normal Windows filesystem access. The restricted sandbox attempt on the earlier source could not read an installed Vite package; the lockfile install itself was unchanged. |
| Earlier full Go suite | Passed on pre-route-correction head `6c24322`: `go test -p=1 -count=1 -timeout=20m ./...` (`internal/generatedingress` 630.723s). The first restricted-sandbox attempt failed on local Go-cache/private-directory access before it established a code result. This earlier pass does not verify the newly integrated Linux route matcher. |
| Post-route-integration full Go suite | PASS on code head `6305bdc` with the corrected row-25 ancestor: `go test -p=1 -count=1 -timeout=20m ./...` passed every package; `internal/generatedingress` completed in 629.512s. This is local fake/passive and unit evidence, not the three physical Docker journeys. |
| Later integrated row-25 correction | After the additive merges through `f1707e9`, the focused exact-mount comparator tests, Docker-tagged compilation, and docs build passed locally. The late parity source also passed the full Go suite in row 25 (`internal/generatedingress` 192.522s). The row-37 full suite was not repeated for this live-test-only and documentation correction. |
| Inherited race-budget correction | Row 25's generated-runtime race package hit the 24-minute Go limit at `f1707e9` without a race report; row 24's repository-wide Linux race job was cancelled at its 60-minute job ceiling. Head `6023a0f` expands only the measured Go, step, and job limits while retaining the same race tests and assertions. Hosted reruns are pending; this local row-37 gate has no Linux race pass of its own. |
| Formatting and diff | The original row-37 changed Go files had no `gofmt -l` output. On the post-route-integration branch, `git diff --check 1e2bcd6..HEAD` and the final working-tree `git diff --check` passed. The route-correction Go files were gofmt-checked in row 25 before integration. |

## Remaining gates and rollback

Hosted CI must run all three physical Docker journeys and the residue scan at
the published head before this can count as stage-start acceptance. Public
cutover, terminal history, transfer-aware readers, recovery, fence release,
Linux race proof, and a physical second-device LAN journey remain open.

If hosted acceptance fails, keep the draft unmerged and diagnose the exact
Docker observation. The branch is test and workflow only, so reverting it needs
no production data rollback. Retain uncertain resources for diagnosis rather
than forcing cleanup.
