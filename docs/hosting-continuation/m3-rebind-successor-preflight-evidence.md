# M3 LAN gateway rebind successor preflight evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-cutover-contract` at `d51eff2`

Branch: `feature/hosting-m3-rebind-successor-preflight` (local and unpublished)

## Scope and invariant

This slice adds a passive, read-only preflight for the approved rebind
successor interface and network plan. It must accept a missing or DHCP-drifted
predecessor address while requiring the exact successor interface/address,
stable host and Docker network observations, and a nonoverlapping internal
network plan. A successful read is informational: a future writer must repeat
the checks while holding the effects lease at its own effect boundary.

No claim writer, migration 034, protected successor artifact, Docker mutation,
URL, allocation transfer, rollback, or fence release is added here. Migration
033 remains dormant and unreleasable.

## Automated evidence

The base `d51eff2` inherits the full Go suite and vet result from `24bbffe`;
the intervening branch changed documentation only. The new worktree baseline
`go test -count=1 ./internal/generatedingress -run
'GatewayV2HostPreflight|GatewayV2NetworkPlan|GatewayRebindPredecessor'`
passed (3.041s).

| Branch check | Result |
| --- | --- |
| `go test -count=1 ./internal/generatedingress -run 'GatewayRebindSuccessorPreflight\|GatewayV2NetworkPlan\|GatewayV2HostPreflight'` | PASS after Docker identity correction, including same-prefix network-ID replacement, intra-inventory ID drift, malformed ID, missing/ambiguous successor, host/prefix/SQLite drift, cancellation, lock-release failure, and no-mutation checks. |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages after the identity correction. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS, compile only; no live Docker test executed. |
| `git diff --cached --check` | PASS. |

Security review found that comparing only flattened Docker prefixes would accept
a network identity replacement with the same subnet. The preflight now brackets
each stable-prefix read with validated, sorted network ID snapshots and compares
the IDs across both full observations. Regression tests exercise replacement
between observations and drift during one inventory read. The production prefix
reader also verifies IDs around its own per-network inspection. The result
fails closed on any discrepancy.
Independent security re-review found the ID replacement and intra-read cases
closed the reported gap and identified no remaining concrete issue in this
passive branch. A transient inventory change that returns to its original
identity can escape non-atomic observations; the future writer must repeat
preflight under deployment-effects admission at the effect boundary.
Independent aggregate code review found no concrete correctness or
maintainability issue in this four-file slice. The reviewer did not run tests;
the executable results above are from the local verification run.

## Remaining gates

This preflight does not prove physical successor routing or exact Docker
container ownership. The contract's migration 034, claim writer, protected
successor history, physical cutover/rollback, hosted Docker CI, Linux race,
and second-device acceptance remain open. No publication, merge, or
deployment is authorized by this evidence.
