# M3 guarded rebind data-volume stage evidence

Date: 2026-10-03

Base: `c9a9124` (`feature/hosting-m3-rebind-config-volume-docker-gate`)

Branch: `feature/hosting-m3-rebind-data-volume-stage` (local and unpublished)

## Scope and invariant

This slice adds only the generation-scoped successor Caddy data volume used for
Caddy's `/data` state. It does not provision or manage an application's external
database. The private writer requires an exact protected sequence-four config
volume binding and reattests that volume and the bound successor network before
any new Docker effect. It then binds the data volume's inspected Docker name,
mountpoint, creation time, and ownership digest in create-only protected
sequence five.

The writer must hold the deployment-effects lease, Manager mutex, and raw
gateway OS lock through fresh prepared-claim, protected-history, predecessor,
pinned-image, host-topology, Docker-census, network, and both-volume
observations. A successful create requires exact command output, physical
readback, stable post-effect observations, and protected readback. Replay may
accept only the exact sequence-five receipt and physical identity. A volume
created before durable binding remains unresolved and fenced, even if its name
and labels appear correct.

No container, host port, route, application database, terminal SQLite
transition, fence release, or public controller caller is added.

## Verification

| Check | Result |
| --- | --- |
| Focused progress and data-volume stage tests | `go test ./internal/generatedingress -run '^(TestGatewayRebindProgress|TestGatewayRebindStageDataVolume)' -count=1` passed (61.401s) with normal Windows filesystem access. After review, the forged sequence-five history and ambiguous Docker create/restart regressions passed with `go test ./internal/generatedingress -run '^(TestGatewayRebindProgressHistoryRejectsForgedSequenceFive|TestGatewayRebindStageDataVolumePreservesAmbiguousEffectAndWriteBoundaries)$' -count=1` (7.726s). |
| Full Go suite, vet, build, formatting, and diff check | Final-source `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` took 268.064s. `go vet ./...`, `go build -buildvcs=false ./...`, `gofmt -l` for the four touched Go files, and `git diff --check` passed. An earlier broad attempt failed in unrelated `cmd/rig-relay` from a Windows TCP connection abort; its isolated retry and the final full run passed. |
| Docker-tagged compilation | `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed. |
| Live Linux Docker data-volume gate | Not implemented or run in this slice. |
| Prerequisite network/config-volume hosted Docker gates | Pending publication and hosted CI. |

QA reviewed the failure boundaries and the forged-history and create-error
restart gaps were closed with tests. Security and final integration reviews
found no blocking code issue for a local commit. These reviews do not establish
hosted Docker acceptance. Docker's idempotent create still has a narrow
exact-name race against an actor with independent Docker-socket control.

## Remaining gates

The successor stopped container, serving transition, route publication,
terminal protected receipt, transfer-aware SQLite transition, recovery, fence
release, public caller, Linux race and live Docker proofs, physical second
device LAN acceptance, PR publication, merge, and deployment remain open.
Create-before-bind crash windows for all three staged resources require
deliberate recovery. Docker's idempotent volume creation assumes no independent
Docker-socket actor races an exact-name resource into Rig's final read/create
interval; a live fault test is required before public activation.
