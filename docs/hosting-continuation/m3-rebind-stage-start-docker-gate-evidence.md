# M3 rebind guarded stage-start Docker gate evidence

Date: 2026-10-04

Base: `feature/hosting-m3-rebind-stage-start` at `b455d42` when implementation
began. The row-25 Linux route correction and passing network-gate evidence
were merged additively through rows 26–36, then into this branch at `6305bdc`.
The later race-budget and live-test parity corrections, with row-25 acceptance
evidence through `f1707e9`, were also merged additively through rows 26–37.
The measured CI timeout correction at row-25 head `6023a0f` was then merged
through the same local chain.

Branch: `feature/hosting-m3-rebind-stage-start-docker-gate`, published as
[draft PR #134](https://github.com/tyhuang9/rig/pull/134).

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

## 2026-10-05: distinct owned addresses for real stage start

The inherited gate at PR #135 head
`e46185ff61b3fa519304e5fa0ce8ab9c1a788acb`
[failed all three journeys and cleanup](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694535).
The direct test failed in 98.89s, lost-acknowledgment in 85.00s, and compensation
in 92.86s. Docker reported all three retained stage containers as `Created`.
The original fixture assigned the successor the same IPv4 and port still held
by the running predecessor. That occupied bind prevents the intended successful
start and also makes the listener-withdrawal assertion false. The original log
did not retain Docker's stderr; a new hosted run must establish the correction.

The fixture now uses two distinct private addresses on a disposable dummy
adapter. Its helper is extracted from the already reviewed handover fixture:
it selects a nonoverlapping prefix, checks exact index/name/alias/dummy kind and
owned addresses before deletion, and retains anything uncertain. Existing host
interfaces are not modified. The predecessor remains live throughout staging,
and all prior protected-state, SQL, request-count and withdrawal checks remain.

The new job-scoped `RIG_RUN_LIVE_GATEWAY_REBIND_STAGE_START_NETWORK=1` is required
alongside the existing Docker opt-in. CI checks `iproute2` and noninteractive
sudo, and its always-run residue check now includes owned dummy adapters.
No production source or runtime safeguard changes in this correction.

Local verification:

- `go test -mod=readonly -tags=integration -run '^$' ./internal/generatedingress ./cmd/hostd`
  compiled both packages (1.051s and 0.967s); it intentionally ran no tests.
- All three named live tests were discovered and explicitly skipped without
  the Docker/network opt-ins, with no failures. This verifies opt-in behavior,
  not Docker acceptance.
- The repository's existing `gopkg.in/yaml.v3` parser accepted the workflow;
  explicit permission and all three required test names were present. All 28
  embedded shell scripts passed Git Bash `-n` syntax checks.
- Independent source review found no blocking address-ownership, cleanup-order
  or workflow regression. Actual Docker start and cleanup remain unverified on
  this Windows host, which has no running Docker daemon.
- `go vet -mod=readonly ./internal/generatedingress`, changed-file `gofmt`
  checks, the docs workflow check, and the docs build passed (VitePress 4.68s).

The correction updates the already authorized draft. It does not authorize a
merge, deployment or a new branch publication.

## 2026-10-05: cleanup after a proved stage start

The distinct-address correction ran at PR #135 head
`f2b7815c9b15f47d328cd4f38f6e32b905307cd2`. Its
[stage-start job](https://github.com/tyhuang9/rig/actions/runs/37381953709/job/112005904184)
reported a cleanup failure in all three named tests (94.52s direct, 89.42s
lost acknowledgment, 91.59s compensation), followed by the residue failure.
Each reported test error was the stopped-container helper rejecting the
sequence-eight configuration inventory. A stage that has actually started
retains Caddy's exact autosave even after a proved stop.

The cleanup helper now calls the existing production inventory reader with
the exact retained sequence-nine start intent. That reader verifies unchanged
protected history before allowing only the approved stage file and its exact,
bounded optional autosave. A never-started sequence-eight fixture continues
to use the strict pre-start inventory. Container ownership, stop proof,
listener withdrawal, immutable history and residue checks remain required.
Only test cleanup changes; production parsers and runtime behavior are unchanged.

Local verification:

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./internal/generatedingress -run '^(TestGatewayRebindStageStartInventoryAcceptsExactAutosaveAfterDurableIntent|TestGatewayRebindStageAutosaveCanonicalConfigMatchesPinnedCaddy|TestGatewayRebindStartedStageArchiveRequiresExactOptionalSnapshot|TestGatewayRebindStartedStageArchiveBoundsTwoMaximumConfigs|TestGatewayRebindStageStartInventoryRequiresDurableUnchangedIntent)$'
go vet -mod=readonly ./internal/generatedingress
go test -mod=readonly -p=1 -tags=integration -run '^$' ./internal/generatedingress ./cmd/hostd
```

All five named autosave regressions passed in 9.056s with no failures/skips.
Static checks passed; integration-tag compilation passed (ingress 0.759s;
hostd reused a cached compile). Captured regression events are in
`$TEMP/m3-stage-cleanup-autosave-regressions.jsonl`. Hosted execution is still
required to accept the complete corrected cleanup journey.

## Earlier verification checkpoints

| Check | Result |
| --- | --- |
| Live-tag compile | Passed on post-route-integration head `6305bdc`: `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress`; the physical tests did not run. |
| Named test discovery | Passed: `go test -count=1 -run '^TestLiveGatewayRebindSuccessorStageStart(LostAcknowledgmentAdopts|ProofFailureWithdraws)?$' -v ./internal/generatedingress`; all three were discovered and explicitly skipped without the Docker opt-in. This is not physical acceptance. |
| Focused stage-start unit tests | Passed after the QA correction: `go test -p=1 -count=1 -timeout=10m -run '^TestGatewayRebindStageStart' ./internal/generatedingress` (`205.641s`). |
| Workflow YAML | Passed with a temporary checker using the repository's `gopkg.in/yaml.v3` dependency; the `rebind-stage-start-live` job parsed and was present. Temporary checker removed. |
| Physical Docker journeys | Not run locally. The initial published stage-start gate failed as recorded above; the distinct-address correction still requires a new hosted result. |
| Prerequisite network-stage hosted gate | PASS at corrected code head `fd68ab8`: [named Docker test and always-run residue job 111619317032](https://github.com/tyhuang9/rig/actions/runs/37264835255/job/111619317032). The later row-25 evidence head `f1707e9` also passed its [named test without a skip and cleanup job 111620115206](https://github.com/tyhuang9/rig/actions/runs/37265095730/job/111620115206) in 46.06s. The later `6023a0f` named network test and cleanup also passed. These jobs establish only the private network effect, not this stage-start gate. |
| Generated-ingress package tests | Passed: `go test -p=1 -count=1 -timeout=20m ./internal/generatedingress` (`648.529s`). This run began before a small assertion was added to the live test, which skips locally; final-source live-tag compilation and named-test discovery passed afterward. |
| Vet and build | Passed on post-route-integration head `6305bdc`: `go vet ./...` and `go build -buildvcs=false ./...`. |
| Documentation build | Passed on post-route-integration source: `pnpm --dir docs build` with normal Windows filesystem access. The restricted sandbox attempt on the earlier source could not read an installed Vite package; the lockfile install itself was unchanged. |
| Earlier full Go suite | Passed on pre-route-correction head `6c24322`: `go test -p=1 -count=1 -timeout=20m ./...` (`internal/generatedingress` 630.723s). The first restricted-sandbox attempt failed on local Go-cache/private-directory access before it established a code result. This earlier pass does not verify the newly integrated Linux route matcher. |
| Post-route-integration full Go suite | PASS on code head `6305bdc` with the corrected row-25 ancestor: `go test -p=1 -count=1 -timeout=20m ./...` passed every package; `internal/generatedingress` completed in 629.512s. This is local fake/passive and unit evidence, not the three physical Docker journeys. |
| Later integrated row-25 correction | After the additive merges through `f1707e9`, the focused exact-mount comparator tests, Docker-tagged compilation, and docs build passed locally. The late parity source also passed the full Go suite in row 25 (`internal/generatedingress` 192.522s). The row-37 full suite was not repeated for this live-test-only and documentation correction. |
| Inherited race-budget correction | Row 25's generated-runtime race package hit the 24-minute Go limit at `f1707e9` without a race report; row 24's repository-wide Linux race job was cancelled at its 60-minute job ceiling. Head `6023a0f` expands only the measured Go, step, and job limits while retaining the same race tests and assertions. At `6023a0f`, repository-wide Linux race passed while the dedicated ingress race job hit its 32-minute limit; this local row-37 gate has no Linux race pass of its own. See the inherited partition correction below. |
| Formatting and diff | The original row-37 changed Go files had no `gofmt -l` output. On the post-route-integration branch, `git diff --check 1e2bcd6..HEAD` and the final working-tree `git diff --check` passed. The route-correction Go files were gofmt-checked in row 25 before integration. |

## Inherited CI partition correction

Rows 26–37 contain row 25's additive `9ad7f78` correction. At the prior
`6023a0f` head, the network-stage Docker test and cleanup passed, and the
repository-wide Linux race job passed; the dedicated ingress race package
still hit its 32-minute timeout. The dedicated job now partitions ingress
tests with complementary `^TestGatewayV2` run/skip filters and runs the other
eight packages unfiltered. Its stable aggregate check requires all three
matrix batches to succeed. No tests or production checks were removed.

Row 25 locally verified all 453 discovered ingress tests ran exactly once
across its two partitions (445 passed, eight existing environment-gated tests
skipped). The other eight packages, all 502 frontend tests, frontend build,
workflow parsing/Bash syntax, and docs build passed. These are row-25 local
results; this longer row-37 test set and its physical Docker journeys still
require hosted acceptance after publication. The new hosted checks at
`9ad7f78` were running at this historical checkpoint. The stack was subsequently
authorized and row 37 was published as draft PR #134.

## Remaining gates and rollback

Hosted CI must run all three physical Docker journeys and the residue scan at
the published head before this can count as stage-start acceptance. Public
cutover, terminal history, transfer-aware readers, recovery, fence release,
Linux race proof, and a physical second-device LAN journey remain open.

If hosted acceptance fails, keep the draft unmerged and diagnose the exact
Docker observation. The branch is test and workflow only, so reverting it needs
no production data rollback. Retain uncertain resources for diagnosis rather
than forcing cleanup.
