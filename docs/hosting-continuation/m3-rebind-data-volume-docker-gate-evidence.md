# M3 rebind data-volume Docker gate

Date: 2026-10-03

Base: `91921d0` (`feature/hosting-m3-rebind-data-volume-stage`)

Branch: `feature/hosting-m3-rebind-data-volume-docker-gate` (local and unpublished)

## Scope and acceptance invariant

This branch adds a hosted Linux Docker gate for the private successor Caddy
`/data` volume effect. It does not provision an application database or add a
public rebind caller. A disposable gateway fixture commits the predecessor,
prepares the rebind claim, and uses production Docker and host-network readers
to bind the successor network, config volume, and data volume in protected
sequences three, four, and five.

The test must prove that the data volume has the exact inspected name, local
driver and scope, empty options, role labels, mountpoint, creation time, and
ownership digest. Earlier protected records and their bytes, network topology,
config volume, prepared SQLite claim, predecessor route/resources, and routed
request count must remain unchanged. A restart replay must preserve exact
protected bytes and physical identity without creating another volume.

Cleanup may remove only the data volume bound in protected sequence five after
fresh physical ownership inspection, then the equivalently verified config
volume and network. An unbound or uncertain resource must be retained for
investigation and cause the CI residue gate to fail. Hosted CI must require
the named live test to pass and reject a skip.

## Verification

| Check | Result |
| --- | --- |
| Base stage | Final-source full Go suite, vet, build, and Docker-tagged compilation passed at `91921d0`; see the stage evidence. |
| Live test discovery and tagged compilation | `go test -list '^TestLiveGatewayRebindSuccessorDataVolumeStage$' ./internal/generatedingress` discovered the exact test. `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed compilation. |
| Local live test invocation | `go test -run '^TestLiveGatewayRebindSuccessorDataVolumeStage$' -count=1 -v ./internal/generatedingress` skipped before Docker access on Windows because `RIG_RUN_LIVE_GATEWAY_V2` is unset. Hosted CI sets it and rejects a skip. No Docker effect ran locally. |
| Full Go suite, vet, build, formatting, and diff check | Final-source `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` took 267.967s. `go vet ./...`, `go build -buildvcs=false ./...`, `gofmt -l` for the new test, and `git diff --check` passed. The workflow YAML parsed with the repository's `gopkg.in/yaml.v3`, and the exact data-volume job was found. |
| Hosted Linux Docker gate and exact cleanup | Pending publication and CI. |
| Prerequisite network/config-volume hosted Docker gates | Pending publication and CI. |

QA review found and corrected a replay that initially reused the same Manager;
the live test now constructs a fresh Manager and production readers for replay.
Security and final integration reviews found no blocking code or CI issue for a
local commit. These static reviews do not establish hosted Docker acceptance.

## Remaining gates

This test cannot prove the physical second-device LAN path or route cutover.
The successor container, terminal protected receipt, transfer-aware SQLite
transition, recovery, fence release, public caller, Linux race proof, merge,
and deployment remain open. Docker's idempotent volume creation assumes no
independent Docker-socket actor races an exact-name volume into Rig's final
read/create interval; the live cleanup inspection/delete interval has the same
host-level Docker-socket trust boundary. A separate fault test is required
before public activation.
