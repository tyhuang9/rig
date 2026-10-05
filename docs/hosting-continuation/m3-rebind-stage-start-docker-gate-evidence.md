# M3 rebind guarded stage-start Docker gate evidence

Date: 2026-10-04

Base: `feature/hosting-m3-rebind-stage-start` at `b455d42` when implementation began; current parent head `4ae53f2` was merged additively before final verification.

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
| Live-tag compile | Passed after the QA correction: `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress`. |
| Named test discovery | Passed: `go test -count=1 -run '^TestLiveGatewayRebindSuccessorStageStart(LostAcknowledgmentAdopts|ProofFailureWithdraws)?$' -v ./internal/generatedingress`; all three were discovered and explicitly skipped without the Docker opt-in. This is not physical acceptance. |
| Focused stage-start unit tests | Passed after the QA correction: `go test -p=1 -count=1 -timeout=10m -run '^TestGatewayRebindStageStart' ./internal/generatedingress` (`205.641s`). |
| Workflow YAML | Passed with a temporary checker using the repository's `gopkg.in/yaml.v3` dependency; the `rebind-stage-start-live` job parsed and was present. Temporary checker removed. |
| Physical Docker journeys | Not run locally. Docker CLI is installed, but the Windows host has no running Docker Engine. Hosted Linux Docker acceptance has not run. |
| Generated-ingress package tests | Passed: `go test -p=1 -count=1 -timeout=20m ./internal/generatedingress` (`648.529s`). This run began before a small assertion was added to the live test, which skips locally; final-source live-tag compilation and named-test discovery passed afterward. |
| Vet and build | Passed: `go build -buildvcs=false ./...`; `go vet ./...` passed again after the QA correction. |
| Documentation build | Passed after the parent documentation fix was merged: `pnpm --dir docs build`. |
| Full Go suite | Passed before the QA correction: `go test -p=1 -count=1 -timeout=20m ./...` (`internal/generatedingress` 687.642s). The correction changed only a live test that skips locally; final-source full-suite verification remains pending. |
| Formatting and diff | Changed Go files have no `gofmt -l` output; `git diff --check feature/hosting-m3-rebind-stage-start..HEAD` passed. |

## Remaining gates and rollback

Hosted CI must run all three physical Docker journeys and the residue scan at
the published head before this can count as stage-start acceptance. Public
cutover, terminal history, transfer-aware readers, recovery, fence release,
Linux race proof, and a physical second-device LAN journey remain open.

If hosted acceptance fails, keep the draft unmerged and diagnose the exact
Docker observation. The branch is test and workflow only, so reverting it needs
no production data rollback. Retain uncertain resources for diagnosis rather
than forcing cleanup.
