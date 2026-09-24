# M2 attested controller-host route

**Branch:** `feature/hosting-m2-attested-local-route`, stacked on unmerged
[draft PR #72](https://github.com/tyhuang9/rig/pull/72). This is an M2
implementation slice, not M2 acceptance. The tested code head is
`df7e0f773855403a5147fd123f11db9845ae9cde`. The branch is published as
[draft PR #73](https://github.com/tyhuang9/rig/pull/73) and remains unmerged.

## Route contract

The authenticated, no-store `GET /api/v1/apps/{appId}/local-route` response
offers a URL only for a verified active generated deployment. The address has
`controller_loopback` scope: it is usable on the Rig controller host through
the loopback-bound Caddy listener. It says nothing about LAN or public access.
The response identifies the immutable deployment, release, accepted plan, and
scoped configuration revisions that are serving, and includes the observation
time. A failed replacement can leave the prior healthy deployment serving;
the route must identify that prior deployment rather than the failed candidate.

The controller requires agreement among the protected ingress route record,
live Caddy container and admin configuration, healthy owned endpoints, active
generated runtime head and components, and succeeded generated deployment
provenance. An absent route, pending switch, Docker/Caddy failure, drift, or
cross-store mismatch yields no URL. The API performs no route repair or
mutation. Verification is a point-in-time observation, not a future uptime
promise. The read-only observation and final active-head comparison use a
15-second context deadline and serialize against ingress switches. The dashboard
disables a cached route after 60 seconds and offers a manual recheck.

## Verification plan and evidence

| Check | Result |
| --- | --- |
| Ingress observer success, protected-state and live-config drift, Docker and endpoint failure tests | `go test -p 1 -count=1 ./internal/controller ./internal/generatedingress` passed. Includes cancellation releasing the switch mutex and callback deadline propagation. |
| Controller auth, no-store, app isolation, provenance and active-head race tests | Included in the focused Go test pass above. Runtime release and plan mismatches, unsucceeded main deployment, and concurrent head change withhold the URL. |
| UI success, stale/mismatch, failed replacement, error, expiry, and focus tests | `pnpm test` in `web`: 400/400 passed; route-focused Vitest: 66/66 passed. `GOFLAGS=-buildvcs=false pnpm e2e`: 3/3 passed. |
| OpenAPI drift, Go vet, web build, embedded assets, and docs | `go run ./cmd/openapi-gen -check`, `go vet ./...`, `pnpm build` in `web`, `scripts/check-embedded.ps1`, `pnpm build` and `pnpm check:accessibility` in `docs`, and the Pages workflow contract check passed. Tagged `live_docker` hostd test compiled with `-run '^$'`. |
| Full serial Go suite | `go test -p 1 -count=1 -timeout=15m ./...` passed on the rebased code head. An earlier run under heavy Windows host load failed five existing `internal/runtime/process` termination timing tests after a 105-second snapshot package run. The process package passed alone (`go test -p 1 -count=1 -timeout=15m ./internal/runtime/process`), then the complete serial suite passed without concurrent builds. No files in that package changed in this slice. |
| Hosted Linux Docker controller, TLS PostgreSQL, Caddy and Chromium journey using the returned URL | Passed on PR #73 head `e8f6fa91e563b2cb02fafdba4a209f11c2c414a3`: [controller run 36058747239](https://github.com/tyhuang9/rig/actions/runs/36058747239) passed in 4m9s. `TestLiveControllerGeneratedDeploymentJourney` passed in 165.38s; Chromium created/read a note through the API-provided route after initial deployment and read it again after controller restart, healthy replacement, and failed bad-CA replacement. Fixture cleanup passed. The local test requires `RIG_RUN_LIVE_CONTROLLER_JOURNEY=1` and a Docker host; neither was available locally. |
| Other hosted gates on the same PR head | [PostgreSQL and Linux race](https://github.com/tyhuang9/rig/actions/runs/36058747072), [disposable Docker hosting and generated-runtime race](https://github.com/tyhuang9/rig/actions/runs/36058747032), [Windows controller](https://github.com/tyhuang9/rig/actions/runs/36058747074), [hosted Chromium](https://github.com/tyhuang9/rig/actions/runs/36058747030), [relay Compose](https://github.com/tyhuang9/rig/actions/runs/36058747083), documentation, fast verification, and relay packaging all passed. PostgreSQL and Linux race ran for 20m5s. |

This slice does not provision a database. The hosted fixture supplies an
application-owned external TLS PostgreSQL service through scoped runtime
secrets. Real GitHub consent, the recipe matrix, Windows Docker Desktop, and
physical LAN checks remain separate M2 acceptance work. No merge or deployment
is authorized by this record.

The first local embedded-assets run could not read pnpm's hardlinked TypeScript
binary under the default Windows sandbox; the identical check passed with
workspace dependency access. The first local Playwright run failed at Go VCS
stamping in the managed worktree; it passed with `GOFLAGS=-buildvcs=false`.
These environment failures did not exercise the route. Hosted Chromium verified
navigation of the returned URL; manual keyboard use of the route controls
remains unverified.
