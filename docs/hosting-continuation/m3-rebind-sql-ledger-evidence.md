# M3 dormant rebind SQL ledger evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-protected-intent` at `9c2dae3`

Branch: `feature/hosting-m3-rebind-sql-ledger` (local and unpublished)

## Scope and boundary

Migration 034 rebuilds the migration-033 rebind claim, event, and roster tables
in one migration transaction. It retains prepared claims and their exact event
and roster rows, adds a typed predecessor source, makes the schema capable of
retaining future terminal claims, and removes event-state uniqueness so a
guarded recovery writer can later revisit a state. It also creates purpose
bound transition-command and allocation-transfer tables. Their insert
triggers block all rows in this slice.

The migration retains prepared-only claim insertion and immutable claim,
event, roster, command, and transfer history. All 18 migration-033 rebind
fences still reject mutations when **any** claim exists. Migration-026 upgrade
profile pins and migration-029 grant profile pins are unchanged. No controller
writer, state transition, Docker effect, URL publication, transfer-aware
reader, or fence release is installed. Migration 034 remains dormant.

## Executable evidence

| Check | Result |
| --- | --- |
| `go test -count=1 ./internal/database ./internal/appaccess` | PASS; database 8.875s, appaccess 36.278s. |
| `go test -count=1 -run 'TestLANGatewayRebind(SQLLedger|Lineage)Migration' ./internal/database` after the two QA acceptance checks | PASS, 0.845s outside the restricted filesystem sandbox. |
| `go test -count=1 -timeout=20m ./...` | PASS outside the restricted filesystem sandbox; `internal/database` 13.193s and `internal/generatedingress` 139.020s. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web install --frozen-lockfile` | PASS; lockfile unchanged. |
| `pnpm --dir web test` | PASS, 19 files and 502 tests. |
| `pnpm --dir web build` | PASS; Vite reported its existing non-failing large-chunk warning. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | PASS with command-scoped Git `safe.directory`; no global Git configuration changed. |
| `git diff --check` and mirrored 034 SHA-256 comparison | PASS; both migration copies have SHA-256 `7b4c3e824b1ec038124ce8005a558c1e40b325834abfd296ff7028de850d40bd`. |

The first focused rerun after the QA checks could not open the sandboxed Go
build cache; the same command passed outside the restricted filesystem
sandbox. The first `go build` attempt failed to obtain VCS status because Git did not
trust the sandbox-owned worktree. Repeating it with a command-scoped
`safe.directory` passed. That failure was environmental, not a code failure.

The populated-033 migration test seeds a committed upgrade and one active LAN
allocation with its access revision, committed grant, runtime head, and roster
entry. It checks all 18 roster fields after 034, the retained claim/event
identity and timestamps, the predecessor upgrade events, and
`pragma_foreign_key_check`. Fresh migration tests compare the public and
embedded SQL byte for byte and assert the 18 fences and 026/029 profile pins.
Negative tests reject direct terminal-claim insertion, unequal timestamps on
a prepared insert, a prior-rebind source without a committed receipt command,
event appends, command and transfer planting, and claim updates.
After 034, a mutation of an existing runtime head is rejected by the rebind
fence. With no claim, a valid administrator can insert an ordinary profile
revision, advance the profile head, and read back that head.

Security review found and resolved the prepared-claim timestamp admission gap
before final checks. Manual aggregate code review and QA review gave GO for
this dormant scope. CodeRabbit review could not run because its WSL CLI was
not authenticated; the manual review covered the staged diff instead.

## Remaining gates

The current startup snapshot accepts only zero or one prepared claim. That is
safe only while claim transitions, terminal inserts, and new ledger rows remain
blocked. A later guarded migration must install receipt-bound transitions and
transfer-aware readers together before it narrows any fence or profile pin.
No live Docker cutover, hosted Linux gate, or physical second-device LAN proof
was run for this branch. The pending M3 draft stack remains unpublished; this
evidence does not authorize publication, merge, or deployment.
