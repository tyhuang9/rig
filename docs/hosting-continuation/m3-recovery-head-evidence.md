# M3 LAN recovery head read: local evidence

The recovery-only controller now serves administrator-only, `no-store`
`GET /api/v1/lan/recovery`. It reports the one LAN grant or disable operation
pinned before the listener started, including its immutable approval digest,
claim state, app, allocation, owner, port, and batch position. It reports no
URL and performs no recovery transition.

The read repeats a full SQLite startup census, checks the startup pin, asks
the gateway observer to prove the protected head and quarantined route state,
then rereads the SQLite census. A missing, advanced, completed, ambiguous, or
changed head returns 503 without partial claim details. The controller remains
pinned until restart; a GET cannot advance the batch.

## Verified locally

- `go test -count=1 -p 1 ./...`: passed across all Go packages with normal
  Windows permissions and a task-local Go cache.
- Focused hostd and controller recovery tests: passed. They cover current
  singular and mixed-batch heads, administrator authorization, normal-mode and
  gateway-upgrade recovery denial, no-store responses, stale/advanced or
  missing heads, changed SQLite census, forged digest, gateway proof failure,
  and the absence of ingress mutation during a read.
- `go run ./cmd/openapi-gen -check`: passed.
- `go vet ./cmd/hostd ./internal/controller`: passed.
- `git diff --check`: passed.
- A read-only security review found no exploitable issue in the endpoint or
  recovery gate. The review did not exercise a live gateway.

The generated TypeScript contract was updated by the OpenAPI generator, but
the web suite was not run in this worktree because it has no installed
`web/node_modules`. A concurrent recovery could advance after the read's
final lock and census check, leaving a stale display; the POST callback
rechecks the exact protected head under the gateway lock before mutation.

This is a local read contract checkpoint. The recovery-only browser journey,
live Docker and process-crash timing, physical second-device LAN checks, and
application-owned external database journey remain unverified M3 acceptance.
No branch in this M3 stack has been published, merged, or deployed.
