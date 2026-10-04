# M3 guarded rebind stopped stage-container evidence

Date: 2026-10-03

Base: `5c87693` (`feature/hosting-m3-rebind-data-volume-docker-gate`)

Branch: `feature/hosting-m3-rebind-stopped-stage-container` (local and unpublished)

## Scope and invariant

This slice adds only a private create-only successor Caddy stage container
after the exact successor ingress network and `/config` and `/data` volumes
have been bound in protected history. The container must remain stopped and
must have no effective host listener or route. It does not copy configuration,
start a container, change the predecessor, modify the prepared SQLite claim,
release the rebind fence, or provision an application's external database.

The writer must hold the deployment-effects lease, Manager mutex, and raw
gateway OS lock while reattesting the prepared claim, protected predecessor,
pinned image, selected host topology, full Docker census, and all prior
bindings. Docker creation and physical readback must match a generation-scoped
configuration with strict security, mounts, network, resource, and port
settings. Protected sequence six may bind only the exact stopped container
identity and configuration digest. Earlier records and bytes remain immutable.
Replay requires that same protected receipt and physical container. An
ambiguous create or create-before-bind crash remains fenced for separately
reviewed recovery; a matching name is insufficient for adoption or cleanup.

## Verification

| Check | Result |
| --- | --- |
| Base branch | Local data-volume gate at `5c87693` passed its final-source Go suite, vet, build, and Docker-tagged compilation. Its hosted Docker run is pending publication and CI. |
| Focused progress and stage-container tests | Final-source `go test ./internal/generatedingress -run '^(TestGatewayRebindProgress|TestGatewayRebindStageContainer)' -count=1` passed (54.798s) with normal Windows filesystem access. Tests cover protected history, stopped create, fresh-Manager replay, post-create and replay drift, and ambiguous boundaries. |
| Full Go suite, vet, build, formatting, and diff check | Final-source `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` took 309.996s. `go vet ./...`, `go build -buildvcs=false ./...`, `gofmt -l` for the four touched Go files, and `git diff --check` passed. |
| Docker-tagged compilation | `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed. |
| Live Linux Docker stopped-container gate | Separate follow-up branch; not implemented or run here. |
| Prerequisite network and volume hosted Docker gates | Pending publication and hosted CI. |

QA review found and closed post-create and replay-drift coverage gaps, including
a real host-interface change and fresh-Manager retries. Security and final
integration reviews found no blocking issue for a local commit. An independent
actor with Docker-socket authority could race an attestation or start the
stopped container afterward; that host-level authority and real Docker
inspection behavior remain outside local proof.

## Remaining gates

The stage config copy, stage start and serving proof, final successor cutover,
terminal protected receipt, transfer-aware SQLite transition, recovery, fence
release, public caller, Linux race proof, physical second-device LAN
acceptance, PR publication, merge, and deployment remain open. No stopped
container is counted as an accepted runtime effect until its hosted Linux
Docker gate passes.
