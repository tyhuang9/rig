# M3 rebind config-volume Docker gate

Date: 2026-10-03

Base: `44f5744` (`feature/hosting-m3-rebind-config-volume-stage`)

Branch: `feature/hosting-m3-rebind-config-volume-docker-gate` (local and unpublished)

## Scope

This branch adds a hosted Linux Docker gate for the private successor config
volume effect. A disposable gateway fixture commits the predecessor, prepares
the rebind claim, and uses the production Docker and host-network readers to
stage the successor network at protected sequence three. It then uses the
production volume adapter to stage sequence four. The test must prove the exact
inspected volume identity and ownership, unchanged network and host topology,
an unchanged prepared SQLite claim and predecessor route/resources, zero extra
routed application requests, and byte-identical protected replay without a
second volume.

The test may clean up only a volume whose exact physical identity is bound in
protected sequence four and whose ownership still matches, followed by the
exact protected sequence-three network. An unbound or uncertain resource is
retained for investigation and must fail the CI residue gate. The job must
require the named live test to pass and reject skip results.

## Verification

| Check | Result |
| --- | --- |
| Live test discovery and tagged compilation | `go test -list '^TestLiveGatewayRebindSuccessorConfigVolumeStage$' ./internal/generatedingress` discovered the exact test. `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed compilation. |
| Local live test invocation | `go test -run '^TestLiveGatewayRebindSuccessorConfigVolumeStage$' -count=1 -v ./internal/generatedingress` skipped on Windows because `RIG_RUN_LIVE_GATEWAY_V2` is unset. Hosted CI sets it and rejects a skip. No Docker effect ran locally. |
| Full Go suite, vet, build, formatting, and diff check | `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` took 221.185s. `go vet ./...`, `go vet -tags live_docker ./internal/generatedingress`, `go build -buildvcs=false ./...`, `gofmt -l` for the new test, and `git diff --check` passed. |
| Hosted Linux Docker gate and cleanup | Pending publication and CI. |
| Prerequisite successor-network hosted Docker gate | Pending publication and CI. |

QA and security reviews found no blocking issue in the live test or cleanup
job. Final integration review approved a local commit after the full Go suite
passed. Those reviews do not substitute for a real hosted Docker run.

## Remaining gates

This test does not prove a physical second-device LAN address change or route
cutover. The successor data volume and container, terminal protected receipt,
transfer-aware SQLite transition, recovery, fence release, public controller
caller, merge, and deployment remain open. Docker's idempotent volume creation
still assumes no independent Docker-socket actor races an exact-name volume
into Rig's final read/create interval; a fault test is required before public
activation.
