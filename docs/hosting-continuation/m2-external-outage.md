# M2 external dependency outage (RUN-11)

**Branch purpose:** prove that a deployed generated frontend and API keep their
route and immutable history while an application-owned external PostgreSQL
dependency disappears, and reconnect after that same dependency returns.

**Base:** `7536c5c09351bdc6212ffe3c58638a62325e4d1a` (`feature/hosting-m2-reviewed-recovery`).
This is a controlled hosted Docker fixture gate. It does not provision a
database or test a live GitHub consent or public network path.

## Verification plan and invariants

The existing controller journey creates a connected controlled GitHub source,
pins its archive and accepted plan/configuration, deploys the `hosting-notes`
frontend and API, and writes a note through Caddy to external TLS PostgreSQL.
The RUN-11 subtest then:

1. Records exact PostgreSQL, API, frontend, deployment, release, route, and
   archive-read identities. Requires one Compose PostgreSQL container with the
   exact project/service labels, then stops only that Docker container ID. The
   external HTTPS dependency stays running.
2. Requires routed note reads to return a bounded `503 database_unavailable`,
   Docker to observe the API as unhealthy through its `/readyz` probe, and the
   frontend to remain healthy and serve its page. The independent HTTPS API
   probe must still work. The local-route API must withhold a verified URL
   while the backend is unhealthy, while the durable serving head and
   application container identities remain unchanged.
3. Requires the authenticated controller to retain the connected GitHub
   source identity and credential generation and the complete initial
   deployment and release records. No application container may be replaced.
4. Starts the same PostgreSQL container ID, waits at most 60 seconds for Docker
   API health and routed note reads to recover, and reads the original note.
   The PostgreSQL and application container IDs, deployment and release IDs,
   source authorization, and archive-read count must remain unchanged. The
   local-route API must then reattest the same serving URL and deployment.

The subtest attempts to restart PostgreSQL on any assertion failure. The
existing journey owns exact fixture and generated-resource cleanup. CI requires
one JSON pass for both the parent journey and its RUN-11 subtest, no fail or
skip event for either, and an independent complete Docker cleanup step.

## Local evidence

On 2026-09-27, the exact base passed
`go test -p 1 -count=1 -timeout=20m ./...` with normal Windows fixture-file
access. An initial sandboxed run failed in unrelated protected-file fixture
tests with `Access is denied`; that run is not a source regression.

On 2026-09-27/28, the changed branch also passed the full Go command above,
`go vet ./...`, `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe`,
`scripts/check-generation.ps1`, and `scripts/check-windows-controller.ps1`.
The changed tagged live package compiled and its staging test passed with
`go test -tags live_docker ./cmd/hostd -run '^TestControllerJourneyStageSource$' -count=1`.
`go vet -tags live_docker ./cmd/hostd` passed. The documentation passed
`pnpm --dir docs build`, `check:accessibility`, and `check:workflow`. The first
accessibility invocation started before the parallel build created `dist`; its
sequential rerun passed. The Windows host has no Docker CLI, so **RUN-11 has
not passed its Docker execution gate locally**.

An independent security review found two false-green/mutation-boundary gaps:
the first version stopped the whole Compose PostgreSQL service rather than an
exact container ID, and it compared only part of the deployment history.
Both were corrected. Focused security rereview found no remaining concrete
issue. The exact hosted Docker outcome remains unverified.

## Risk and remaining evidence

The outage deliberately stops a disposable, application-owned database
container. No production controller, schema, provisioning path, or deployed
database is changed. The test does not infer GitHub authorization from workload
health; it reads the controller's source connection separately. It checks
Docker's live health state rather than treating a verified local route as
application readiness.

Hosted Ubuntu Docker evidence must include the exact source head, the named
`TestLiveControllerGeneratedDeploymentJourney/ExternalDependencyOutage` JSON
pass, its parent journey pass, and the complete cleanup gate. Until then this
is a locally compiled candidate, not RUN-11 acceptance or an M2 exit claim.
