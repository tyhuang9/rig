# M3 prepared rebind Docker predecessor evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-immediate-rollback-poison` at `61c3d38`

Branch: `feature/hosting-m3-rebind-prepared-docker-attestation` (local and unpublished)

## Scope and invariant

This slice adds a passive postclaim attestor for exactly one prepared
migration-033 rebind claim. While holding the generated-ingress Manager and
gateway OS locks, it compares the complete SQLite startup snapshot, committed
protected v2 predecessor and immutable history, v1 source, and journal-bound
Docker ownership and static route configuration across two observations. It
rereads SQLite and protected state after the second Docker observation to
catch drift during inspection.

The predecessor's old host address may be absent. The passive rebind Docker
observer now skips every stage/final HTTP route and status probe, including
requests that could reach hosted applications. It retains exact Docker,
network, endpoint identity, live Caddy admin configuration, and restart
configuration reads. The attestor does not prove old URL reachability or live
application serving. This proof is informational and cannot authorize a
successor effect without the future deployment-effects lease, claim writer,
and postclaim admission chain. No SQLite mutation, protected artifact write,
Docker mutation command, URL publication, fence release, or migration is part
of this slice.

## Automated evidence

| Check | Result |
| --- | --- |
| `go test ./internal/generatedingress -run 'Test(InspectGatewayRebindDockerSelectsPassivePolicy\|ObserveGatewayV2ServingProbes\|GatewayRebindDocker\|InspectGatewayRebindPreclaimWithDocker\|InspectGatewayRebindPreparedDockerPredecessor)' -count=1` | PASS. Covers policy selection, zero passive probe callbacks, normal serving callback dispatch, running/stopped predecessor, claim/Docker drift, inspector errors, second-observation SQLite/protected/source drift, cancellation, lock release, and no mutation with injected observations. |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages after the final policy-selection seam edit. |
| `go vet ./...` | PASS after the final edit. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS, compilation only. No live Docker test executed. |
| `git diff --cached --check` | PASS after staging. |

Security review identified that the earlier production rebind observer sent
`HEAD` and `GET /` requests to hosted applications during a supposedly passive
proof. The new policy suppresses all stage/final route, status, and host HTTP
probe callbacks in both preclaim and prepared-claim inspection. Normal v2
serving inspection still uses those probes and retains its route/404/host
proof requirements. A focused test verifies the rebind observer selects the
passive policy; callback tests verify that passive dispatch invokes none of
the five probes. Security re-review found no remaining application-forwarding
path in the passive observer.

The prepared attestor's Docker observations are injected in local tests. The
local Docker daemon was unavailable, so no production Docker inspection or
application request-counter test ran. A hosted Docker test with a running app
counter is still needed to prove the exact public entrypoints send no app
requests in a real Docker environment. CodeRabbit was unavailable locally;
the independent aggregate code review is manual. Security and QA re-review
found no remaining local blocker for this passive ownership slice.

## Remaining gates

The existing successor preflight must be repeated under the effects lease at
admission. Effects lease and gateway lock integration, actual claim insertion,
migration 034, transfer-aware readers, protected successor history, cutover
and rollback, hosted Docker/race checks, and a real
second-device LAN proof remain open. This evidence does not authorize
publication, merge, or deployment.
