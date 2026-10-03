# M3 LAN gateway rebind quiescence census evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-predecessor-attestation` at `21d5db6`

Branch: `feature/hosting-m3-rebind-quiescence-census` (local and unpublished)

## Scope and invariant

This slice adds a read-only, point-in-time SQLite census of deployment and job
statuses. It reports whether those rows were terminal at one snapshot; it does
not determine whether a future LAN rebind writer may begin. A recognized
terminal status is required for
every row; queued, executing, paused, unknown, unreadable, or cancelled reads
must fail closed. The entire table is read without a UI pagination limit.

This standalone read is not authorization to insert a claim. A future writer
must hold the deployment effects lease, then repeat the census inside the
same SQLite write transaction that inserts its prepared claim. The future
path must preserve the effects lease → Manager mutex → gateway OS lock →
SQLite lock order and must separately prove the protected predecessor.

No claim writer, migration 034, production caller, Docker effect, URL,
allocation transfer, history change, or fence release is added here.

## Automated evidence

The base `21d5db6` passed `go test -count=1 -timeout=20m ./...`,
`go vet ./...`, and tagged live-Docker test compilation before this branch.
The new worktree baseline `go test -count=1 ./internal/appaccess` passed.

The branch passed:

```text
go test -count=1 ./internal/appaccess -run 'GatewayRebindQuiescence|EvaluateGatewayRebindQuiescence'
                                                  PASS (0.653s)
go test -count=1 ./internal/appaccess             PASS (14.350s)
go test -count=1 -timeout=20m ./...             PASS (all packages; appaccess 22.580s)
go vet ./...                                      PASS (no diagnostics)
git diff --check                                  PASS
```

The focused tests cover every recognized job and deployment status, empty
tables, unknown/NULL/corrupt status values, more than 200 rows in each table,
cancellation, a read error, a concurrent update from a second database handle
between the two table reads, and no persisted change from a successful census.
The concurrent-handle test observes the original snapshot in the first census
and the updated nonterminal states in a fresh census.

The transaction-scoped evaluator accepts both the read transaction and the
existing `BEGIN IMMEDIATE` transaction type, so a future claim writer can reuse
the exact status classifications and decision. A test uses a real migrated
database to check both terminal and nonterminal results through an immediate
transaction and confirms no SQLite row or change-count mutation.

Security review found no exploitable issue in this passive read. A separate
manual code review identified the transaction reuse and wording issues above;
both were resolved and the full suite and vet rerun. CodeRabbit was unavailable
in WSL, and
automatic approval review rejected its remote-script installer because it
would make persistent system changes; no CodeRabbit result is claimed.

## Remaining gates

This census does not prove live Docker topology or old-interface reachability.
LAN-08 must still handle an old NIC that has disappeared or DHCP drifted.
The protected successor history, physical cutover and rollback, hosted Docker
CI, Linux race, and second-device acceptance remain open. No merge or
deployment is authorized by this evidence.
