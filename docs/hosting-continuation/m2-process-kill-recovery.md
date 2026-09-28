# M2 controller process-kill recovery candidate

**Purpose:** prove that a replacement can recover from loss of the controller
OS process at each durable generated-runtime boundary, using the retained
controller data root, the same accepted job and immutable pins.

**Branch:** `feature/hosting-m2-process-restart`, based on reviewed-recovery
`7536c5c09351bdc6212ffe3c58638a62325e4d1a`. This page records the local
candidate and the hosted acceptance gate. It does not claim hosted acceptance
until the exact Linux Docker job and cleanup have passed on this branch.

## Invariants and verification plan

- Run an application-owned PostgreSQL fixture through the existing scoped
  runtime secret. Do not provision a database or replay a migration here.
- Accept one replacement at a time with a distinct scoped configuration
  revision and immutable release. Preserve the exact job ID, input bytes,
  reviewed plan/configuration/release/deployment pins and release count.
- Kill a separate controller test process with the operating-system kill while
  the persisted runtime is at build, candidate start, committed route, drain,
  runtime success or main deployment success. Retain the same SQLite data root
  and recover through the real runtime composition and worker in a fresh
  process. Require the original job to converge on its second attempt.
- Attest the previous and candidate route against the production ingress
  observation and Docker container IDs. Request the routed version marker and
  retained note. At uncertain route boundaries retain both slots and require
  an authenticated resume. After convergence require one active candidate,
  the exact previous slot stopped, and no wrong-slot deletion.
- Fail closed on mismatched head, invalid provenance and uncertain migrations.
  A prior migration recorded as `running` is never automatically replayed;
  RUN-08 separately owns migration-execution acceptance. Rollback is to stop
  this candidate and retain the prior reviewed-recovery branch; this change
  has no schema migration or history rewrite.

Automated local checks cover recovery replay and fail-closed cases, including
the two finalization crash windows, plus the process barrier matchers and
tagged test compilation. The hosted job must report exactly one pass and no
fail/skip for the top-level journey and each of six named boundaries, then
prove complete owned Docker cleanup. Linux Docker and browser behavior cannot
be established by Windows compilation.

## Implementation

`generatedrecovery` now recognizes a succeeded runtime linked to a
nonterminal main deployment or job, as well as in-progress runtime phases.
It validates the exact persisted pins and serving head. An uncertain committed
route pauses for reconciliation with both possible serving slots retained.
`generatedexecutor` treats a matching already-succeeded runtime as
finalization: it completes the main deployment without rebuilding, rerouting,
or deleting a container. A mismatched active head pauses instead.

The process-kill test starts each controller worker in a separate test-binary
process with the existing production composition and persisted data root. A
test-only command/progress barrier writes a mode-0600 marker after the targeted
durable state; the parent verifies it, kills and joins the process, observes
traffic and identities, then starts a new process. The small child manifest
contains paths and IDs only; scoped secrets remain in the existing protected
data root. Child output is not echoed into CI diagnostics.

The six boundaries are `before_build`, `before_candidate_start`,
`after_route_commit`, `before_drain_stop`, `after_runtime_success`, and
`after_main_success`. The latter two distinguish runtime completion from main
deployment completion before the job result is recorded. Exact marker checks
make accidental missed subtests a failing CI gate.

## Local evidence, 2026-09-27

The baseline at `7536c5c` passed the full Go suite with normal Windows fixture
permissions. A pre-fix regression for a runtime already succeeded while the
main deployment was still nonterminal failed at recovery: zero generated
deployments were preserved. A second regression covers main deployment
success before job completion. Both pass after the production fix.

| Command | Result |
| --- | --- |
| `go test -p 1 -count=1 ./internal/generatedrecovery ./internal/generatedexecutor` | Passed after production fix |
| `go test -p 1 -count=1 -timeout=20m ./...` | Passed with normal Windows fixture permissions |
| `go test -tags live_docker -count=1 ./cmd/hostd -run '^TestControllerProcessKillBoundaryControls$'` | Passed; test-only barrier controls |
| `go test -tags live_docker ./cmd/hostd -run '^$'` | Passed; tagged compilation only |
| `go vet ./...` and `go vet -tags live_docker ./cmd/hostd` | Passed |
| `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed |
| `pwsh -NoProfile -File scripts/check-windows-controller.ps1` | Passed; DPAPI, session, secure temp, descendant-process and release-path protections |
| `pnpm --dir web build` and `pnpm --dir web test` | Passed; 400 tests across 16 files |
| `pnpm --dir docs build`, `pnpm --dir docs check:accessibility`, `pnpm --dir docs check:workflow` | Passed; accessibility check ran after build completed |

The Linux Docker process-kill journey, hosted cleanup and Linux race checks
have **not run** for this branch. The process-kill gate is a CI acceptance
requirement, not local acceptance evidence. CodeRabbit CLI was unauthenticated
and did not review the source; an independent manual review is required. A
manual security audit found no Critical, High or Must Fix issue, but did not
execute Docker.
