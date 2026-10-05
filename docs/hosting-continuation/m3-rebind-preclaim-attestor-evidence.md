# M3 LAN rebind preclaim predecessor evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-successor-preflight` at `e4861a0`

Branch: `feature/hosting-m3-rebind-preclaim-attestor` (local and unpublished)

## Scope and invariant

The read-only preclaim path accepts an unpersisted rebind proposal only when
SQLite has no rebind claim, its exact current profile has a committed 026
upgrade, both action-bound approvals currently belong to administrators, its
successor profile is absent, and its complete proposed roster matches current
allocations, access revisions, grants, and runtime heads. The Manager then
compares two independently read SQLite/protected predecessor observations
under the gateway lock. The protected comparison requires the exact committed
v2 identity and all, and only, the proposed LAN bindings.

This result cannot authorize a claim or an effect. A future writer must hold
the deployment-effects lease, add an exact Docker ownership proof, repeat the
checks in its immediate claim-insert transaction, then attest the inserted
claim before any successor artifact or Docker effect. Migration 033 remains
dormant and unreleasable. No claim writer, migration 034, Docker mutation,
allocation transfer, URL, or fence release is added.

## Automated evidence

| Check | Result |
| --- | --- |
| Base `e4861a0` | Clean worktree with full Go suite, vet, and tagged Docker compile already passing. |
| `go test -count=1 ./internal/appaccess ./internal/generatedingress -run 'GatewayRebindPreclaim'` | PASS after adding administrator-demotion and protected-history replacement cases. |
| `go test -count=1 ./internal/appaccess ./internal/generatedingress` | PASS before those two extra negative tests; the later focused run passed. |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages after the final code and test changes. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS, compilation only; no live Docker test executed. |

The first focused appaccess run failed because a test attempted to update a
runtime head that existing SQL correctly forbids. The test now presents a
validly redigested stale proposal, and the focused and full suites pass. This
was a test-fixture error, not a pre-existing product failure.

The aggregate branch diff received a manual self-review of authorization,
roster completeness, protected matching, cancellation, and read-only behavior.
Independent specialist review was unavailable because the session's agent
thread limit was reached. This branch must not be treated as writer-ready on
that basis.
The Codex Security diff-scan tool also rejected this worktree before creating
a scan, so no plugin security-scan result is claimed.

## Remaining gates

Exact predecessor Docker ownership, effects admission, the claim-insert
transaction, migration 034, transfer-aware readers, live Docker/LAN cutover,
Linux race, and second-device acceptance remain open. A transient state change
that returns to its original values between non-atomic observations can escape
this informational inspection; the future writer must repeat checks at its
effect boundary. No publication, merge, or deployment is authorized by this
evidence.
