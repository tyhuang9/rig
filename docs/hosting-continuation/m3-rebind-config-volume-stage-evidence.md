# M3 guarded rebind config-volume stage evidence

Date: 2026-10-03

Base: `2dfb1de` (`feature/hosting-m3-rebind-network-docker-gate`)

Branch: `feature/hosting-m3-rebind-config-volume-stage` (local and unpublished)

## Scope and invariant

This slice stages only the generation-scoped successor Caddy config volume
after the exact successor ingress network has been bound in protected progress
sequence three. It must bind the volume's observed Docker name, mountpoint,
creation time, and ownership digest in a create-only sequence-four record. It
does not create a data volume or container, publish a port, change a route,
write a terminal SQLite transition, release the migration-034 fence, or expose
a public controller caller.

The stage holds the deployment-effects lease, Manager mutex, and raw gateway OS
lock through fresh prepared-claim, protected-history, predecessor, pinned
image, successor-network, host-topology, Docker-census, and volume observations.
The exact pre-effect proof must be repeated adjacent to volume creation. A
successful create requires exact command output, physical readback, stable
post-effect observations, and a protected sequence-four receipt. Replay may
accept only that bound receipt and exact physical volume; it must not create a
second volume. A resource created before the receipt is durably bound remains
unresolved and fenced, even if its name and labels appear valid.

## Verification

| Check | Result |
| --- | --- |
| Focused progress and config-volume tests | `go test -count=1 -timeout=10m ./internal/generatedingress -run '^(TestGatewayRebindStageConfigVolume|TestGatewayRebindProgressSequenceFour|TestGatewayRebindProgressOptionalConfigVolume)'` passed on the final QA-adjusted source (39.810s). The combined old/new progress, network, and config-stage suite passed before the QA-only test additions (73.760s). |
| Full Go suite and vet on final source | `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` took 214.176s. `go vet ./...` passed. |
| Docker-tagged compile | `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed. |
| Build and source hygiene | `go build -buildvcs=false ./...`, `gofmt -l` for the four changed Go files, and `git diff --check` passed. |
| Live Docker config-volume gate | Not implemented or run in this slice. |
| Prerequisite successor-network hosted Docker gate | Pending publication and hosted CI. |

Security review found no exploitable issue in this private stage. QA identified
and verified added coverage for byte-exact replay, post-create SQLite claim
drift, and unexpected containers. Final integration review found no blocking
issue for a local commit; it did not establish hosted acceptance.

## Remaining gates

The successor data volume, stopped container, serving transition, route
publication, terminal protected receipt, transfer-aware SQLite transition,
recovery, fence release, public caller, hosted Linux race and live Docker
proofs, physical second-device LAN acceptance, PR publication, merge, and
deployment remain open. The network and volume create-before-bind crash windows
require deliberate recovery work; neither is automatically adopted by name.
Docker `volume create` is idempotent. The stage assumes the Docker socket is
controlled by Rig during its final read/create interval; an actor with direct
Docker daemon authority could race an exact-name, exact-label volume into that
interval. This assumption needs a live fault test before a public caller is
added. The local fake-driver tests do not establish Linux Docker behavior.
