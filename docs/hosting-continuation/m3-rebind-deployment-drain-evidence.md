# M3 LAN gateway rebind deployment admission evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-runtime-fence` at `936d236`

Branch: `feature/hosting-m3-rebind-deployment-drain` (local and unpublished)

## Scope and invariant

Real Compose and generated-runtime workers acquire a persistent, private
per-data-root `deployment-effects.lock` before assigning a queued job. They
hold it through executor cleanup and any job-state persistence, then release
it. Startup deployment and job recovery use the same admission. The admission
checks the fresh migration-033 rebind fence while holding the effects lock;
an active, corrupt, or unreadable fence fails closed. Waiting for the lock is
cancelable and does not assign a job. The lock file is never unlinked during
normal operation and a process exit releases the operating-system lock.

An owner shutdown may leave a job `assigned` or `running` after executor
cleanup and lock release. Startup recovery handles that state. A future rebind
writer must acquire this effects lock first, then inspect and drain nonterminal
jobs under it before taking the gateway OS lock and writing a claim in SQLite.
Lease release alone is not proof that jobs are terminal.

This branch adds only passive admission and recovery coordination. It does
not write a rebind claim, relax migration-026 profile pins, transfer a gateway,
publish a successor URL, or perform a cutover. The proposed writer lock order
is effects lock, ingress manager mutex, gateway OS lock, then SQLite. The
writer and its drain/rollback checks are not implemented, so an independently
inserted claim is not yet atomic with a deployment already in progress.
Startup composition and ingress recovery before `prepareRuntimeWorker` are
still outside the effects lease and need review before activating a writer.

## Automated evidence

The base `936d236` passed `go test -count=1 -timeout=20m ./...` before this
change. After implementation, these checks passed:

```text
go test -count=1 ./internal/runtime/deploymenteffects ./internal/jobs ./cmd/hostd
PASS (deploymenteffects 0.722s, jobs 15.875s, hostd 9.517s)

go vet ./...
PASS
```

The final full suite also passed after the cancellation regression and comment
correction:

```text
go test -count=1 -timeout=20m ./...             PASS (all packages)
go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedimage ./internal/generatedingress
                                                  PASS (compiled; no tests run)
```

Tests cover private lock-file identity and permissions, same-process and
subprocess contention, cancellation, crash release, startup recovery ordering,
fence-read failure and lock release, admission failure without job assignment,
successful terminal persistence before release, two workers sharing the real
lock, and owner cancellation while executor cleanup holds the lock. The last
test confirms that a running job remains visible for startup recovery after
the lock releases. The existing repository fence test covers a validated
prepared claim and incomplete lineage; production admission uses that same
fence function. There is no end-to-end claim-writer contention test because
this slice does not include a claim writer.

## Remaining gates

The Windows host has CGO disabled, so no local race run is available. Linux
lock code was cross-compiled but not run on Linux.
Live Docker and physical second-device acceptance have not run for this
branch. `docker info --format '{{.ServerVersion}}'` failed because the local
Docker daemon pipe was absent; sandbox access to the local Docker config was
also denied. Hosted CI has not run because this branch is unpublished. A separate
same-user process with write access to the private working directory could
replace a Unix advisory-lock pathname; the current trust boundary assumes
private controller directory ownership and no hostile process under that user.

Before a rebind writer is activated, verify the protected predecessor and
successor history, the cross-process drain and startup-composition gap, the
prepared-claim availability effect, Linux race behavior, Docker journeys, and
rollback. No merge or deployment is authorized by this local evidence.
