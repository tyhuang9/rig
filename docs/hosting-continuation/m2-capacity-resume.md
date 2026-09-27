# M2 nondestructive capacity pause and resume

**Purpose:** qualify capacity admission and same-job resume while an existing
deployment continues serving through the real controller/runtime boundary.

**Candidate branch:** `feature/hosting-m2-capacity-resume`, based on the unmerged
recipe-matrix candidate `bf782685aed81e3d63ffe8e1c3c625622795237f`.
**Implementation revision:** `7f9baeb17d4661274ddc03a437c9cb2b3076f9a8`.
This branch is local and unpublished. Local contract verification passes;
hosted qualification has not run on this revision. This is not a RUN-07
acceptance pass.

## Required behavior

- Insufficient replacement capacity pauses the job with the existing useful
  capacity disposition. It must not stop active containers or switch traffic.
- Resume continues the accepted job and retains its immutable source, plan,
  configuration, release and deployment identities once materialized.
- Freeing capacity permits the replacement to complete through the normal
  production admission, runtime, health and ingress path.
- Repeated resume requests and worker recovery must not duplicate the job,
  erase history, blindly repeat completed work, or remove the serving slot.
- No operator-facing capacity override or change to runtime limits is needed
  to exercise controlled fault injection in tests.

## Verification plan

Use existing production controller composition, durable SQLite stores, job
worker and generated executor. Add a locally runnable contract check for
pause/persistence/resume and an opt-in hosted Docker gate with a real serving
version, controlled admission pressure, route checks during the pause, and a
successful resumed replacement. Keep any deterministic fault seam confined to
test dependency injection; distinguish that evidence from real host resource
exhaustion. Require the exact named live-test pass event in CI and owned cleanup.

The high-risk boundaries are immutable deployment pins, admission reservations,
job attempts, active-head generations and slot cleanup. Include failure and
repeat-resume behavior, and review the aggregate diff before committing.

## Pre-change baseline on 2026-09-27

`go test -p 1 -count=1 ./cmd/hostd ./internal/generatedexecutor
./internal/generatedruntime ./internal/jobs` passed on base `bf782685`, using
Go 1.27.0 on Windows, `GOFLAGS=-mod=readonly`, the existing locked dependencies
and normal user filesystem access. No process depended on the reused clean
checkout, and the completed Next.js source branch was retained.

## Composition checkpoint

An unexported optional factory wraps the existing ingress capacity source only
when supplied by internal composition callers. Production startup supplies no
factory and continues to use ingress. A nil factory result is rejected by the
existing engine validation before executor/worker construction. The same engine
still supplies the authorization gate and its aggregate reservations.

`TestGeneratedCompositionCapacitySourceFactory` passed with default construction,
exact ingress delivery to the factory and nil-source rejection. After that
change, `go test -p 1 -count=1 ./cmd/hostd ./internal/generatedruntime` and
`go vet ./cmd/hostd` passed. Targeted review found no unresolved security issue
in these two files. No configuration/API option, schema or runtime-limit change
is included.

## Durable worker evidence

`TestCapacityPausedDeploymentResumesWithRecordedPinsAndOneReplacement` uses real
SQLite jobs, deployment/runtime state, plan/configuration stores, release
materialization, artifact persistence, repository authorization and engine
capacity admission. It proves:

- Two insufficient-capacity attempts pause before compiler, candidate,
  migration or route work. Resume queues the same job without advancing its
  attempt until a worker claims it.
- Changing the current configuration head while paused does not change the
  accepted configuration, plan, release or deployment identities.
- The third attempt succeeds after capacity is released, with one compile,
  candidate and route operation, one deployment and one ready release.
- Worker cancellation and bounded join execute before inspecting test doubles,
  including when the wait helper fails.

Compiler, candidate runtime, migration and routing are test doubles in this
contract. It starts without a serving version and does not establish Docker
operation, retained real traffic or full controller restart recovery. Capacity
snapshots are deterministic test inputs, not real host resource exhaustion.

The artifact fixture is persisted through the canonical artifact repository;
the candidate fixture uses a valid container identity. SQLite foreign keys and
provenance validation remain enabled. No shared fake or production validator
was weakened to satisfy the fixture.

## Hosted controller qualification added

The existing mandatory `TestLiveControllerGeneratedDeploymentJourney` now pauses
its healthy same-source replacement after immutable provenance is recorded.
Controlled pressure wraps the real capacity source through the internal seam;
releasing it delegates to the actual ingress snapshot and normal reservation
path. While paused, the test requires:

- The original active head, slot, generation and application container IDs.
- No build invocation and unchanged builder container/network identities.
- Fresh attested route identity plus API/database and browser note reads.
- Exactly one materialized replacement source with pinned plan/configuration.

Authenticated resume must complete the original job on exactly its next attempt
and retain the paused deployment/release identities. Exact build and archive
counts, two-entry history, resumed route, scoped containers and persisted notes
are asserted. The subsequent unhealthy replacement case remains in the journey.
The immediate resume response permits the worker to advance before HTTP
serialization; the final success and attempt assertions remain exact.

The existing hosted workflow already requires the exact named pass event for
this continuous journey. No timeout, workflow bypass or new permissive skip was
added. This source compiles locally, but the new live behavior is unrun here
because Docker is unavailable.

## Verification on 2026-09-27

Commands used Go 1.27.0 on Windows, `GOFLAGS=-mod=readonly` and a temporary Go
cache. Checks requiring Windows filesystem/process access ran with normal user
permissions. Builds used a process-scoped Git `safe.directory` setting; no
global trust setting or `-buildvcs=false` workaround was introduced.

| Check | Result |
| --- | --- |
| Focused durable capacity test, then full `./internal/generatedexecutor` package | PASS |
| Composition factory/default/nil-source tests and affected runtime package | PASS |
| `go test -p 1 -count=1 -timeout=20m ./...` | PASS: full Go suite |
| `go vet ./...` | PASS |
| `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | PASS |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | PASS: OpenAPI/routes and migration mirrors |
| `go test -p 1 -count=1 -tags live_docker ./cmd/hostd -run '^$'` | PASS: compile only |
| `go vet -tags live_docker ./cmd/hostd` | PASS |
| `pnpm --dir web typecheck` and `pnpm --dir web test` | PASS: 400 tests |
| `pwsh -NoProfile -File scripts/check-embedded.ps1` | PASS: production build and exact asset hashes |
| `pwsh -NoProfile -File scripts/check-windows-controller.ps1` | PASS: expected Windows protection tests executed |
| Documentation build, accessibility and workflow checks | PASS |
| Go formatting and aggregate diff whitespace checks | PASS |
| Linux race and new hosted Docker journey | UNRUN |

The Windows race command could not start because CGO is disabled. Initial
restricted-sandbox source-fixture checks failed on filesystem protection; the
existing release materialization regression and new capacity contract passed
with normal user permissions. The initial command build encountered Git's
checkout trust check and passed with the process-scoped setting above. These
environment failures were not hidden by changing dependencies or validation.

## Limits and rollback

Production has no new capacity control exposed to operators. Reverting the
internal seam and qualification tests restores the previous composition; there
is no schema/data migration or new infrastructure to undo. Existing immutable
history and runtime limits are unchanged.

M2 still requires hosted qualification on its integrated head and the separate
live GitHub authorization/archive walkthrough. Migration uncertainty, negative
private-network probes and interrupted runtime recovery remain distinct gates.

The recovery audit found two pre-existing RUN-10 gaps in
`generatedrecovery.loadRecoveryBinding`: its paused-job allowlist omits capacity
pauses, and its strict deployment-input decoder omits current reviewed-pin
fields. These findings are from code inspection; executable reproductions and
the focused recovery fix are pending. A jobs-only recovery check cannot prove
that a capacity pause survives full controller composition/recovery. This
slice must not make that claim.
