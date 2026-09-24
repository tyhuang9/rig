# Hosting continuation: M2 controller journey candidate

**Recorded:** 2026-09-24. **Branch:** `feature/hosting-m2-controller-journey`,
based on the unmerged M1 draft candidate. This is an M2 implementation slice,
not an M2 acceptance claim. Draft PR [#71](https://github.com/tyhuang9/rig/pull/71)
targets the unmerged M1 branch. Neither PR has been merged.

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

The first hosted Docker run ([workflow 36032619684](https://github.com/tyhuang9/rig/actions/runs/36032619684),
head `58389f4`) reached the durable deployment job but failed with
`invalid_source` before image builds. The cleanup step passed. Investigation
found that the workflow's pnpm install leaves package links under the fixture's
`node_modules`; the local release materializer correctly rejects links. The
controller test now stages a clean fixture source, keeping the installed copy
only for application-owned schema preparation. The second hosted run
([workflow 36033350599](https://github.com/tyhuang9/rig/actions/runs/36033350599),
head `1e429d0`) reached real image builds and container readiness, then failed
with `health_failed`. Its cleanup step passed. The next run records only
component names and Docker health transitions to identify the failing
component without exposing application output or secret values. That run
([workflow 36034083663](https://github.com/tyhuang9/rig/actions/runs/36034083663),
head `b77facb`) showed the API healthy and the static frontend exited with
code 1 before serving. Cleanup passed. A bounded error-category probe on the
controlled frontend's startup log is pending in the next hosted run.
The fourth hosted run ([workflow 36034611177](https://github.com/tyhuang9/rig/actions/runs/36034611177),
head `b79d07b`) classified that startup failure as `MODULE_NOT_FOUND`;
the API again became healthy and cleanup passed. The next diagnostic checks
the accepted static run command and logs only the missing module's basename.
The fifth hosted run ([workflow 36035148372](https://github.com/tyhuang9/rig/actions/runs/36035148372),
head `688cee7`) confirmed the accepted static command and identified the
missing module basename as `static.mjs`; the API was healthy and cleanup
passed. The next run distinguishes the application's workspace from Rig's
runtime library path and probes that exact library file in the stopped
container without printing its contents.
The sixth hosted run ([workflow 36035720070](https://github.com/tyhuang9/rig/actions/runs/36035720070),
head `ad5eb92`) located the error at Rig's `/usr/local/lib/rig/static.mjs`.
Docker could copy that file from the stopped container even though Node exited
with `MODULE_NOT_FOUND`; API health and cleanup passed. The next bounded probe
records only the static module and parent directory modes/owners to resolve
the apparent access mismatch.
The seventh hosted run ([workflow 36036347990](https://github.com/tyhuang9/rig/actions/runs/36036347990),
head `0a5a6e8`) found the exact mismatch: both `static.mjs` and its newly
created parent `/usr/local/lib/rig` had mode `0444` and root ownership.
The non-root runtime could read the file but could not traverse its parent.
The generated image recipe now explicitly sets the parent directory to
`0555` after copying the file. The post-fix hosted gate is pending.

The QA and security reviews of the controller harness found no confirmed
exploit. Their actionable gaps were addressed: the deployed API now probes
the HTTPS fixture, a nonlocal Docker context is rejected before mutation,
ingress cleanup is registered before composition, idempotent replay and exact
release/configuration pins are asserted, and the Buildx record is checked after
removal. Broader image-layer and frontend-asset secret coverage remains in
the M1 hosted gate. The Windows host has no Docker CLI, so hosted CI is the
authority for the Docker journey.

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

- Complete the hosted controller journey gate on the corrected branch head.
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
