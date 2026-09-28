# M2 combined local integration candidate

Date: 2026-09-28. This is a disposable, local-only candidate on
`scratch/hosting-m2-combined-acceptance`. Its tested source revision is
`14e53407b0f5445e5444d6b47f250755f246a3e3`. No delivery branch, PR base,
or remote ref was changed by this integration work. No PR was merged or
deployment published.

## Source provenance

The candidate starts at reviewed-recovery draft PR #79 revision
`7536c5c`. Exact source commits were copied into new local commits, in this
order, without rewriting the source branches:

| Slice | Source revision | Status at integration |
| --- | --- | --- |
| Bootstrap CLI, [draft PR #76](https://github.com/tyhuang9/rig/pull/76) | `24cdd3b` | Hosted checks passed on that branch; live user setup remains open. |
| Gateway readiness, [draft PR #77](https://github.com/tyhuang9/rig/pull/77) | `26a2837` | Hosted checks passed on that branch. |
| Network isolation, [draft PR #80](https://github.com/tyhuang9/rig/pull/80) | `474d160` | Controlled RUN-09 Docker and Linux race gates passed on that branch. |
| Migration uncertainty, [draft PR #81](https://github.com/tyhuang9/rig/pull/81) | `c3e7e1c` | Controlled RUN-08 Docker and Linux race gates passed on that branch. |
| Process-kill recovery, local RUN-10 branch | `65e3b5d` | Locally reviewed and checked; hosted Docker is pending. |
| External dependency outage, local RUN-11 branch | `c17221a` | Locally reviewed and checked; hosted Docker is pending. |

The source heads above are independent evidence. Their green checks do not
establish behavior on this combined source revision.

## Reconciliation on the combined candidate

- The gateway-readiness scenario runs after the durable capacity pause and
  resume. Its Docker command observer is the restarted controller's observer;
  the build counter used by the capacity checks remains in that observer.
- Controller restart uses a fixture listener disjoint from the external
  PostgreSQL and HTTPS fixture ports. Network isolation assertions also run
  after the capacity-paused restart.
- RUN-11 executes only in the ordinary controller journey, so the six RUN-10
  process-kill boundaries do not each repeat a destructive database outage.
- When the external database is stopped and API health becomes unhealthy, the
  local-route API must withhold attestation. The test checks an unchanged
  durable serving head, deployment/release history, source connection, and
  container IDs, then requires the original URL to be reattested after the
  same database container recovers.
- The controller workflow requires exact pass events, with no fail or skip,
  for the source-staging test, ordinary journey, outage subtest, process-kill
  journey, and each of the six named kill boundaries. Both hosted jobs retain
  always-run owned Docker cleanup checks.

## Executed local verification

All commands below passed on the combined source tree. The documentation-only
commit adding this record does not change executable source.

| Check | Result |
| --- | --- |
| `go test -p 1 -count=1 -timeout=15m ./...` | Passed twice, including after the RUN-11 attestation correction. The ordinary Go suite does not execute opt-in live Docker tests. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | Compiled the hosted test packages; no live Docker test executed. |
| `go test -tags live_docker -v -count=1 -timeout=5m ./cmd/hostd -run '^Test(ControllerProcessKillBoundaryControls|ControllerJourneyStageSource|ControllerJourneyControllerProbeTargetUsesLoopbackControllerPort|ControllerJourneyAppControllerProbeClassifierFailsClosed|ControllerJourneyAppProbeAttestationBoundaries|HostingMigrationFixtureAnalyzerContract|ControllerJourneyGitHubProviderRetainsImmutableRevisions)$'` | Seven named control tests passed, including eight process-kill boundary-control subtests. |
| `go vet ./...` and `go vet -tags live_docker ./cmd/hostd ./internal/generatedingress` | Passed. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed. |
| `pnpm --dir web install --frozen-lockfile`, `pnpm --dir web test`, `pnpm --dir web build` | Locked install, 400 tests in 16 files, and production TypeScript/Vite build passed. |
| `pnpm --dir web e2e` | Three Chromium tests passed, including bootstrap through the zero-argument `hostd bootstrap-token` command. The browser fixture uses fake runtime. |
| `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check-generation.ps1` and `scripts/check-embedded.ps1` | OpenAPI/migration mirrors and exact embedded production assets passed. |
| `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check-windows-controller.ps1` | Windows DPAPI, session, secure temporary files, process-tree cancellation, and short-path release checks passed. |
| `pnpm --dir docs build`, `pnpm --dir docs check:accessibility`, `pnpm --dir docs check:workflow` | Passed after the status and outage documentation corrections. |
| `git diff --check 7536c5c..HEAD` | Passed before this evidence-only commit; worktree was clean. |

The Windows host has no `docker` executable, so it cannot run the real
container, Caddy, browser-to-PostgreSQL, migration, network, process-kill, or
outage journeys on this combined revision. The independent security review
found no remaining actionable issue after the route-attestation fix; the
manual code review found two CI/documentation issues that were fixed. These
reviews do not replace hosted execution or CodeRabbit source review (the
draft PR bot skips source review).

## M2 exit remains open

A separately authorized draft publication is needed to run hosted Docker,
Chromium, PostgreSQL/Linux race, process-kill, outage, migration, network,
and complete cleanup gates on the **exact combined head**. Record its Rig SHA,
fixture source SHA, plan/configuration/deployment/release and image IDs,
browser note transaction, Docker versions, named test events, and cleanup.
The live administrator and GitHub authorization/archive walkthrough against
the user-approved `tyhuang9/test-repo` remains a separate manual gate. No
managed database or Neon provisioning is part of this candidate. M2 is not
accepted or ready to merge from these local results.
