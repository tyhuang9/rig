# M3 rebind stage-config hosted Docker gate evidence

Date: 2026-10-04

Base: `7763971` (`feature/hosting-m3-rebind-stage-config-copy`)

Branch: `feature/hosting-m3-rebind-stage-config-docker-gate` (local and unpublished)

Scope commit: `1ba86c0`

## Scope

Two named live tests exercise sequence-seven empty config inventory and
sequence-eight guarded config copy against a disposable Linux Docker daemon in
hosted CI. One requires a direct same-call copy, readback, and protected
receipt. The other simulates a lost acknowledgment after a real copy and
requires fresh-manager adoption without recopy. Both require the exact bound
stopped container, probe-and-404-only bytes in `/config/stage.json`, exact
protected receipt, fresh-manager replay, and unchanged predecessor traffic,
route, SQLite claim, host topology, and earlier protected records. Cleanup
removes only fully attested generation-scoped resources without force. An
uncertain resource remains for the always-run CI residue check to fail.

The gate does not start the successor container, publish a route, transition
SQLite, provision an application database, merge, or deploy.

## Verification

| Check | Result |
| --- | --- |
| Two named live tests discovery and expected local skip | Passed: `go test -count=1 -timeout=4m ./internal/generatedingress -run '^TestLiveGatewayRebindSuccessorStageConfigCopy(LostAcknowledgmentAdopts)?$' -v`; both skipped without Docker opt-in. |
| Docker-tagged compile | Passed: `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress`. |
| Focused stage-config-copy unit tests | Passed: `go test -count=1 -timeout=10m -run '^TestGatewayRebindStageConfigCopy' ./internal/generatedingress` (51.010s). |
| Full Go suite | Passed on final dual-path source with package serialization: `go test -p=1 -count=1 -timeout=20m ./...` (`internal/generatedingress` 417.506s). Parallel runs failed in unchanged `cmd/rig-relay`; see below. |
| Vet and build | Passed on dual-path source: `go vet ./...`; `go build -buildvcs=false ./...`. |
| Formatting and diff | Passed: `gofmt -l` reported no files; `git diff --check` reported no whitespace errors. |
| Workflow syntax and required pass/skip/residue behavior | Passed with a temporary `gopkg.in/yaml.v3` checker (`go run .tmp-codex-stage-config-copy-gocache/yaml_check.go`; output: `workflow YAML parsed; stage-config-copy job present`); the temporary checker was removed. Each step requires its own JSON test pass and rejects skip; an `always()` step checks labeled and generation-scoped resources. Static QA and security review found no blocker. |
| Hosted Linux Docker acceptance | Not run. Publication and CI require authorization. |

No web files changed; web tests and production build were not rerun for this
test-and-workflow branch. Local Windows Docker was unavailable. Manual review
confirmed the workflow names both required tests, checks their individual JSON
pass events, rejects skips, and runs residue checks after failure. Security
review found no blocker. QA found and then confirmed closure of a missing
direct-success live path. Final integration review found no code or CI blocker
after that correction, conditional on the final checks and hosted execution.

Two parallel full-suite runs after adding the direct-success case observed a
Windows TCP connection abort in unchanged `cmd/rig-relay` test
`TestRealTCPDeadlineExemptionInvalidConnectAndKeepAlive` at
`server_tcp_test.go:58` (`wsarecv: An established connection was aborted by
the software in your host machine`). The exact test passed immediately when
run alone with `go test -count=1 -timeout=3m -run
'^TestRealTCPDeadlineExemptionInvalidConnectAndKeepAlive$' ./cmd/rig-relay`.
The full suite passed with `-p=1`, including `cmd/rig-relay`. The second
parallel run was stopped after the same failure was reported. This
host-dependent failure is recorded separately from the gateway gate; no relay
files changed in this branch.

## Remaining gates

The complete M3 rebind still needs stage start/serving, cutover, terminal
receipts, transfer-aware SQLite transitions, recovery, fence release, a public
caller, Linux race proof, and physical second-device LAN acceptance. This local
branch claims no hosted Docker pass.

## Rollback

If hosted acceptance fails, leave the draft unmerged and correct or revert the
test-and-workflow commit after diagnosis. No runtime data rollback is needed:
the branch does not modify production behavior or persistent application data.
Keep any uncertain Docker resource for investigation rather than forcing
cleanup in a failed live run.
