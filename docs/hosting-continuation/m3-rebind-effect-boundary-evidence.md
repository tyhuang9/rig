# M3 prepared rebind effect-boundary attestor evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-sql-ledger` at `2eaf35d`

Branch: `feature/hosting-m3-rebind-effect-boundary-attestor` (local and unpublished)

## Scope and invariant

This slice adds a private, read-only attestor for one prepared initial rebind.
It acquires the deployment-effects lease, then the raw generated-ingress
gateway lock. While both are held, it compares two complete observations of
the prepared SQLite claim and roster, protected predecessor and installed
successor intent, exact predecessor Docker ownership, and approved successor
network inventory. It then rereads the SQLite and protected anchors. A result
contains only scalar identities and digests and is returned only after both
locks release successfully.

The fresh canonical SQLite digest must match the installed intent's database
digest. The reader reconstructs the expected protected intent from the fresh
claim, predecessor, and network observation and compares it to the installed
create-only bundle. A drift, cancellation, missing/forged source, ambiguous
Docker ownership, or lock/release error returns no evidence. The attestor has
no production caller or effect path; migration 034's prepared fence remains
active. Its digest is unkeyed and becomes stale when the locks release, so a
future writer must re-attest while holding its own full lock chain and must
never use this result as standalone authorization.

## Executable evidence

| Check | Result |
| --- | --- |
| Focused `TestGatewayRebindEffectBoundary` suite | PASS, 11.790s after the QA test corrections and final predecessor-store comparator change. |
| Two-app and subprocess lease contention cases repeated five times | PASS. |
| `go test -count=1 ./internal/generatedingress` | PASS, 146.408s. |
| `go test -count=1 -timeout=20m ./...` | PASS on the final source outside the restricted filesystem sandbox; generated-ingress completed in 144.943s. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web install --frozen-lockfile` | PASS; lockfile unchanged. |
| `pnpm --dir web test` | PASS, 19 files and 502 tests. |
| `pnpm --dir web build` | PASS; existing non-failing Vite large-chunk warning. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | PASS with command-scoped Git `safe.directory`; no global Git configuration changed. |
| `gofmt -l` on the two new Go files | PASS, no unformatted files. |

Tests use a real one-app and two-app prepared SQLite/grant/runtime/protected
fixture with passive injected Docker and host-network observations. They reject
forged intent/database digests, changed claim or event ledger, stale committed
grant or runtime head (including a second-app-only grant change), wrong Docker
ID or labels, two individually valid but different predecessor Docker states,
network drift after a valid first read, changed protected history,
cancellation, gateway/lease acquisition and release faults, and modified
returned evidence. A stopped but exactly owned predecessor passes, matching
the DHCP-drift starting state. The success path asserts two Docker inspections
and four Docker-ID reads occur under the gateway lock. The cross-process test proves a separate
process cannot acquire the deployment-effects lease during the attestation
checkpoint. Success and failure tests assert that no routed-request or Docker
mutator command was executed. A source search found the private attestor is
called only by its tests.

The focused attestor suite and full repository suite were both rerun after the
QA test corrections and the stronger predecessor-store comparison.

Security, manual quality, and QA reviews gave GO for the read-only scope.
Security found no effect bypass or exposed endpoint. CodeRabbit did not run
because its WSL CLI was unauthenticated; the manual review covered the branch
instead. Local race testing was unavailable because `CGO_ENABLED=0`
and no `gcc` command was installed. Hosted Linux race and Docker gates, live
network observation, and physical second-device LAN acceptance remain open.

## Remaining gates

This evidence does not authorize a Docker effect, SQLite transition, URL
publication, migration-035 fence release, PR publication, merge, or deployment.
The guarded writer must keep the effects lease and gateway lock through its
own fresh attestation, effect, and exact claim transition. Transfer-aware
readers, protected progress and terminal receipts, recovery, hosted Linux
checks, and physical second-device proof remain to be implemented.
