# M3 public passive rebind Docker hosted gate evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-preclaim-successor` at `6e462b8`

Branch: `feature/hosting-m3-rebind-public-passive-docker-gate` (local and unpublished)

## Scope and acceptance contract

This test-only slice adds a required hosted Linux Docker gate for both public
passive predecessor attestors. The test creates a committed v2 gateway and a
counted application in a disposable Docker fixture. It seeds matching SQLite
profile, upgrade, access, grant, terminal deployment, runtime, and zero-claim
rebind roster state. It confirms the committed protected gateway and SQLite
predecessor agree before calling the public entrypoints.

The test calls `InspectGatewayRebindPreclaimDockerPredecessor` twice while the
claim is absent. It then inserts a test-owned prepared migration-033 claim and
roster from the validated preclaim snapshot, confirms the prepared snapshot,
and calls `InspectGatewayRebindPreparedDockerPredecessor` twice. After every
call, the counted application's routed request total must stay unchanged.
Each attestor also rejects the opposite claim phase without incrementing that
counter. An ordinary serving-route proof must increase the same counter as a
positive control. The fixture enforces a disposable default local Linux Docker
host and cleans up its owned resources.

No production rebind writer, claim insertion path, migration, Docker effect,
URL publication, or fence release is added by this branch. The test's direct
prepared-claim insert is confined to its disposable fixture.

## Executable evidence

| Check | Result |
| --- | --- |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages after the final review edits; the named live Docker test skipped on Windows without `RIG_RUN_LIVE_GATEWAY_V2`. This is not Docker acceptance. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./...` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web install --frozen-lockfile` | PASS. |
| `pnpm --dir web test` | PASS: 19 files, 502 tests. |
| `pnpm --dir web build` | PASS; Vite reported an existing non-failing large-chunk warning. |
| `go build ./cmd/hostd` | PASS with command-scoped Git `safe.directory` for this sandbox-owned worktree. No global Git configuration changed. |
| Hosted `TestLiveGatewayRebindPublicPassivePredecessor` | PENDING publication and Linux Docker CI execution. No hosted counter result is claimed. |

The first sandboxed full Go run hit Windows temporary-file, socket, and Docker
access restrictions; the elevated rerun passed. The workflow requires an exact
JSON test pass, rejects a skip, and retains unconditional Docker cleanup.
Code review found redundant test digest calculation and unhelpful mismatch
errors; those were corrected. QA found missing terminal deployment state and
opposite-phase rejection checks; those were added, and its re-review passed.
Security review found no exploitable issue in this test or workflow.

## Limits and remaining gates

The counter measures requests routed to the application. It does not detect
gateway-local 404 probes for a removed host or container-local health checks;
separate probe-policy unit tests cover callback suppression. The Windows run
cannot establish the live Linux Docker result. The hosted gate must pass before
this acceptance claim is made.

The M3 rebind writer, migration 034, transfer-aware readers, cutover, recovery,
Linux race, and physical second-device LAN acceptance remain open. Migration
033 remains dormant and unreleasable. This evidence does not authorize
publication, merge, or deployment.
