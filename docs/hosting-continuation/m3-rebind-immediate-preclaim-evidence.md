# M3 LAN rebind immediate preclaim evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-docker-ownership` at `70a7c95`

Branch: `feature/hosting-m3-rebind-admission-rehearsal` (local and unpublished)

## Scope and invariant

The appaccess preclaim and full job/deployment census now run on one pinned
SQLite connection after `BEGIN IMMEDIATE`. The new informational API repeats
the exact zero-claim profile, approval, roster, allocation, grant, and runtime
head checks before counting every job and deployment. It always rolls back and
returns no result on any validation, cancellation, or rollback error. It
inserts no claim, event, roster entry, successor profile, or fence transition.

The existing read-only snapshot and startup readers retain their queries;
their narrow query interfaces now accept either `*sql.Tx` or the repository's
immediate-transaction connection. A future claim writer must acquire the
deployment-effects lease and Manager/gateway locks first, repeat the Docker
predecessor and successor checks, and insert a claim in the same immediate
transaction. This rehearsal is not that writer and cannot authorize effects.

## Automated evidence

| Check | Result |
| --- | --- |
| `go test ./internal/appaccess -run 'TestGatewayRebindImmediatePreclaim' -count=1` | PASS after the final test improvements. Covers complete terminal census, zero persisted claim/events/roster, existing-claim rejection, nonterminal-job rejection, cancellation, second-handle writer contention, post-commit visibility, and reservation release. |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages before the final test-only assertion improvements. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS, compilation only; no live Docker test ran. |

The first focused run exposed a test-fixture mismatch: the existing rebind
fixture deliberately leaves a deployment job and deployment nonterminal. The
success case now terminalizes those rows; the failure case retains a running
job. A second test fixture initially tried to add a duplicate job for the
same application and was changed to update the existing job in a competing
transaction. The focused checks pass after those fixture corrections.

Code review found no reader behavior regression and requested precise lock
error assertions plus a second-handle release check; both were added and
passed. Security review found no bypass in this informational slice. It did
identify a pre-existing risk in `immediateTransaction.Rollback`: a SQL rollback
failure may return an unknown-state connection to the pool. The new API
returns an error and clears its result, but the helper must discard that
connection and be fault-tested before a production claim writer uses it.

## Remaining gates

The effects lease, Manager/gateway locks, successor preclaim preflight, exact
Docker ownership recheck, actual claim insert and postclaim attestation,
migration 034, transfer-aware readers, guarded cutover, hosted Docker/race,
and second-device LAN acceptance remain open. No publication, merge, or
deployment is authorized by this evidence.
