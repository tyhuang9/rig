# M3 passive rebind Docker hosted gate evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-prepared-docker-attestation` at `b0bd439`

Branch: `feature/hosting-m3-passive-docker-live-gate` (local and unpublished)

## Scope and acceptance contract

This test-only slice adds a required hosted Linux Docker gate for the
production passive rebind Docker observer. A running, committed v2 gateway
routes to a counted test application. The test reads the application's
request counter from a test-owned file inside its container, then performs
two passive Docker inspections and requires an unchanged count. The normal
serving route proof is the positive control: it must increase the count.
The passive observations must retain exact static predecessor ownership and
configuration while all route, 404, and host-publication probe flags remain
false.

The app writes each count to a replacement file and atomically renames it
before responding. Routine `/health` checks from its own loopback are excluded
from the routed count. The counter is read through Docker, not through an
application HTTP endpoint.
The test owns and cleans up its Docker resources. Its fixture performs a v2
upgrade inside a disposable test root. No production code, rebind claim
writer, migration, URL publication, or rebind effect is added.

## Evidence

| Check | Result |
| --- | --- |
| `go test -count=1 ./internal/generatedingress -run '^TestLiveGatewayRebindPassiveDockerSendsNoApplicationRequests$' -v` | PASS package; the named test SKIPPED as expected because `RIG_RUN_LIVE_GATEWAY_V2` is unset on this Windows host. This is compilation evidence, not Docker acceptance. |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages; the live test remains skipped. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./internal/generatedingress` | PASS, compilation only. |
| `git diff --cached --check` | PASS after staging. |
| Hosted `TestLiveGatewayRebindPassiveDockerSendsNoApplicationRequests` | PENDING publication and CI execution; no pass or request-counter result is claimed. |

The existing `hosting-gateway-v2-ci.yml` job now requires an exact test pass
and rejects a skip. Its job name stays unchanged for check continuity, and its
cleanup step still runs after failure. Security review found no actionable
fixture or CI issue. Manual code review found a counter-file read race; the
atomic rename closes it, and re-review found no remaining code issue.

## Limits and remaining gates

This tests the production Docker observer directly. It does not exercise the
public preclaim or prepared-claim entrypoints with matching migration-033
SQLite and protected fixtures. Those public paths need a separate hosted
acceptance gate before a production claim writer or successor effect is
considered ready. The original M3 cutover, transfer, recovery, and second
device LAN gates remain open. This evidence does not authorize publication,
merge, or deployment.
