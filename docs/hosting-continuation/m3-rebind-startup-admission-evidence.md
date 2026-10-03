# M3 LAN gateway rebind startup admission evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-deployment-drain` at `5fcdbb9`

Branch: `feature/hosting-m3-rebind-startup-admission` (local and unpublished)

## Scope and invariant

The controller's real Compose and generated-runtime startup must acquire the
persistent deployment effects lease after opening SQLite and before gateway
startup inspection. It must hold the lease through LAN recovery quarantine,
runtime composition and ingress recovery, final gateway reattestation, and
interrupted deployment/job recovery. It must release the lease successfully
before launching the normal worker or a recovery-only server. The worker then
uses the same admission function before each job assignment. Acquisition and
release failures stop startup; a fence failure invokes only the existing
exact-owned emergency gateway stop.

This closes a startup ordering gap in the preceding passive-admission slice.
It does not prepare or release a rebind claim, change migration 026 or 033,
write protected successor history, transfer allocations, cut over Docker
bindings, or publish a URL. A future claim writer must still inspect/drain
nonterminal jobs under the effects lease and attest protected predecessor and
successor history. This branch does not by itself activate a rebind.

## Automated evidence

The base `5fcdbb9` passed `go test -count=1 -timeout=20m ./...` before this
change. Focused checks on the final startup wiring passed:

```text
go test -count=1 ./cmd/hostd -run '^(TestDeploymentEffectsAdmission|TestPrepareRuntimeWorker|TestRuntimeComposition|TestGeneratedComposition)'
PASS (cmd/hostd 5.970s; rerun after final test cleanup)

go test -count=1 ./cmd/hostd
PASS (cmd/hostd 9.093s; run before the final release-wrapper adjustment)

go test -count=1 -timeout=20m ./...
PASS (all packages; generatedingress 84.923s)

go vet ./...
PASS (no diagnostics)

go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedimage ./internal/generatedingress
PASS (compiled; no live Docker tests run)
```

The focused tests cover a held lease during generated ingress recovery,
deployment and job recovery, followed by release before worker execution;
same-directory lock contention and reacquisition; corrupt-fence rejection
without retaining the lock; release on missing dependencies or recovery
failure; and release failure preventing worker start. A small release wrapper
records one release attempt, so an explicit failure is not retried by deferred
cleanup. The production `runServer` acquisition and early-return paths were
also reviewed in the aggregate diff. There is no process-level startup test
that drives the full production `runServer` through Docker.

## Remaining gates

The local Docker daemon and physical second-device path are unavailable for
this branch. The Windows host has CGO disabled, so a local Go race run was not
available. Hosted CI has not run because the branch is unpublished. The
cross-store claim writer, protected history attestation, and rollback remain
future work. No merge or deployment is authorized by this local slice.
