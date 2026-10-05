# M3 immediate-transaction rollback connection evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-admission-rehearsal` at `94e09ec`

Branch: `feature/hosting-m3-immediate-rollback-poison` (local and unpublished)

## Scope and invariant

`immediateTransaction` manually starts SQLite transactions with `BEGIN IMMEDIATE`.
If SQL `ROLLBACK` fails, the connection may still contain an open transaction.
The helper now returns the rollback error and signals `driver.ErrBadConn` through
`sql.Conn.Raw`, causing `database/sql` to discard that driver connection. It
does not put an unknown-state connection back in the pool. If the discard
signal itself fails unexpectedly, the helper returns both errors and keeps the
connection pinned. Successful rollback still closes the `sql.Conn` normally.

This changes the shared helper, not the rebind admission rules or any claim
writer. A private executor parameter exists solely to inject a SQL rollback
failure in a deterministic test while a real SQLite transaction is open.

## Automated evidence

| Check | Result |
| --- | --- |
| `go test ./internal/appaccess -run 'TestImmediateTransactionRollback' -count=1` | PASS. Normal rollback keeps the connection reusable and removes the row. Injected rollback failure preserves the error, discards the sole pooled connection, does not retry, and a fresh connection sees no uncommitted row. |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages. |
| `go vet ./...` | PASS. |
| `git diff --check` | PASS. |
| `go test -race -count=1 -timeout=5m ./internal/appaccess -run '^TestImmediateTransactionRollback'` | NOT RUN: this Windows Go installation has `CGO_ENABLED=0` and no `gcc`; Go rejected `-race` before executing tests. |

The failure test injects an executor error before SQLite executes `ROLLBACK`;
it verifies the connection-discard path with an actual open transaction. It
does not simulate a database driver that returns an error after applying a
rollback. No live Docker, LAN device, or public production-observer acceptance
ran for this helper change.

Security review and an independent manual code review found no actionable
issue in this narrow diff. CodeRabbit did not run: the CLI is unavailable in
the local WSL environment. If `Conn.Raw` itself fails unexpectedly, the
connection stays pinned and the process may need a restart to restore
availability; this path still avoids returning unknown transaction state to
the pool.

## Remaining gates

The production rebind claim writer still needs an effects lease, Manager and
gateway locks, a repeated predecessor/successor proof under those locks, the
claim insert in the same immediate transaction, and postclaim attestation.
Migration 034 and transfer-aware readers remain unimplemented. This evidence
does not authorize publication, merge, or deployment.
