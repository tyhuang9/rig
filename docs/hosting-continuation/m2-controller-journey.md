# Hosting continuation: M2 controller journey candidate

**Recorded:** 2026-09-24. **Branch:** `feature/hosting-m2-controller-journey`,
based on the unmerged M1 draft candidate. This is an M2 implementation slice,
not an M2 acceptance claim. The branch has not been published or merged.

## Implemented slice

- The saved generated-application setup continues from accepted source and plan
  review through scoped configuration, local access review, a guarded deployment
  request, and the durable job/deployment result. Setup resumes by application
  ID. The UI does not claim an access URL because the controller API does not
  currently attest one.
- An opt-in Linux Docker gate uses the authenticated controller HTTP API,
  production generated-runtime composition and worker, immutable local source
  snapshot, public-package fixture, real image builds, Caddy ingress, and
  application-owned external TLS PostgreSQL and HTTPS services. It checks
  controller idempotency replay, plan/configuration and source pins, scoped
  container environment, API note create/read, frontend public label, and
  ownership-checked cleanup. The gate is wired to a pull-request-only workflow.
- The test uses the existing `examples/hosting-notes` fixture. Its database
  schema and credentials remain application-owned; Rig does not provision a
  database. There is no managed database or Neon provisioning path.

## Verification so far

| Check | Result |
| --- | --- |
| `go test -count=1 -timeout=15m ./...` | Passed locally on Windows, including controller, generated runtime, source snapshot, and job packages. |
| `go test -tags live_docker ./cmd/hostd -run '^TestPrepareRuntimeWorkerRecoversInOrderBeforeStartingOneWorker$' -count=1` | Passed locally; compiles the new tagged live harness and runs a focused existing test. It does **not** execute the Docker gate. |
| `go vet -tags live_docker ./cmd/hostd` | Passed locally. |
| Frontend TypeScript, production Vite build, and full Vitest suite | Passed after review fixes: 373 tests in 14 files. The Windows worktree used the installed TypeScript, Vite, and Vitest Node entrypoints directly because the local pnpm 11.19 shim attempted to replace the linked 11.22 dependencies. |
| Real-controller Chromium (`web/e2e/hostd.spec.ts`) | Passed with the new setup navigation, heading focus, local-access review, fake-runtime deploy guard, and the existing Compose create/cancel path. The first run found a missing initial heading focus after query loading; a later run found a query-key collision that disabled legacy Compose deploy. Both were fixed. |
| Full Playwright suite | Passed: 3 tests, including the real-controller journey and two source-connection/focus journeys. Local runs set `GOFLAGS=-buildvcs=false` because the managed worktree's Git metadata is restricted to the spawned browser-test Go build. |
| Embedded dashboard hash comparison | Passed after the final Vite build; the controller-served file set and SHA-256 hashes match `web/dist`. |

The QA and security reviews of the controller harness found no confirmed
exploit. Their actionable gaps were addressed: the deployed API now probes
the HTTPS fixture, a nonlocal Docker context is rejected before mutation,
ingress cleanup is registered before composition, idempotent replay and exact
release/configuration pins are asserted, and the Buildx record is checked after
removal. Broader image-layer and frontend-asset secret coverage remains in
the M1 hosted gate. The new controller gate itself has not yet run on Docker:
the Windows host has no Docker CLI, and this M2 branch is local.

Frontend review caught and fixed a job API response mismatch, loss of an
uncertain idempotency key after configuration drift, inaccessible continuation
failure feedback, and hidden known-job results when current setup becomes
unavailable. The UI now compares the recorded deployment pins with the
reviewed revisions and warns if they differ. The backend still has no
deployment precondition for the reviewed revisions and no lookup by
idempotency key. If a response is lost and the setup changes, the UI preserves
the unresolved key, blocks automatic replay, and requires an explicit
history-check decision before starting a new request. It cannot automatically
prove whether the original request reached the controller.

## Open M2 acceptance work

- Run and debug the hosted controller journey gate on this exact branch.
- Exercise a real browser through frontend, Caddy, backend, and the external
  fixture database; verify note persistence after controller restart and
  healthy replacement.
- Add controlled GitHub archive/connection materialization to the continuous
  harness and perform a separate live GitHub authorization walkthrough.
- Prove unhealthy replacement retains the old serving version, and cover the
  supported Node, Vite, Next.js, and package-manager recipe matrix.
- Verify manual setup, backend interface binding, capacity pause, migration
  failure, private-network isolation, and interrupted controller recovery.
- Expose an attested route URL and a revision-pinned deployment precondition
  through the controller before the UI can assert that the reviewed revision
  and displayed URL are the ones actually serving.

M1's direct-engine hosted run established its scoped configuration gate, but
it does not substitute for these controller and browser checks. M2 remains
open until its exit gate has executable evidence on the final candidate.
