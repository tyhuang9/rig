# M2 standalone recipe matrix candidate

**Branch:** `feature/hosting-m2-recipe-matrix`, based on the unmerged
[Next.js cache draft PR #74](https://github.com/tyhuang9/rig/pull/74).
This slice qualifies remaining generated Node/Vite/package-manager shapes; it
does not complete M2 acceptance or authorize a merge or deployment.
The implementation at `1e5821d780976cbc1039445a61bf9e7c326c2706`
was published with explicit user authorization as
[draft PR #75](https://github.com/tyhuang9/rig/pull/75).

## Coverage and boundaries

The standalone npm/Node API fixture has a real pinned Express dependency,
immutable version and readiness endpoints, a container-interface listener, and
a synthetic runtime-only secret-presence check. The Vite-only Yarn 4 fixture
has a real pinned Vite lockfile, an immutable static build and public marker.
Both are single-package repositories. The existing canonical hosting-notes
fixture covers a pnpm workspace with API and frontend; the existing Next.js
fixture covers npm and a runtime server. Existing hosted manual-setup gates
cover undetected source. The new hosted test builds both added fixtures through
the production generated recipe, starts them through its entrypoint with their
accepted commands, and requests responses through published Docker ports. The
canonical notes and Next.js hosted journeys separately
exercise Caddy and controller boundaries.

Yarn writes `.yarn/install-state.gz` during local installation. The compiler
excludes only that generated file from source identity and image staging.
Checked-in Yarn configuration and cache content remain eligible, subject to
the existing credential scan and build-context limits.

## Verification evidence

| Check | Result |
| --- | --- |
| Locked package installation | `npm ci` installed the standalone API's 68 packages. `corepack yarn install --immutable` passed for the Yarn 4 fixture after generating its lockfile; `corepack yarn build` produced the static assets with `VITE_BUILD_MARKER=vite-public-A`. |
| Local API behavior | The standalone Node API started and answered `/health` and `/version` with the expected immutable version and no secret present. The process was stopped after the check. |
| Fixture and source-boundary tests | `go test -p 1 -count=1 ./internal/generatedimage ./internal/projectanalysis ./internal/sourceinspection` passed. It stages both fixtures through the compiler, excludes local dependency/build output and Yarn install state, and confirms that generated Yarn state does not change the source structural fingerprint. |
| Tagged Docker-test compilation | `go test -p 1 -tags live_docker -run '^$' ./internal/generatedimage` and `go vet -tags live_docker ./internal/generatedimage` passed. The local Windows host has no Docker CLI; the real image/port journey ran in hosted CI below. |
| Repository checks | `go test -p 1 -count=1 -timeout=15m ./...`, `go vet ./...`, `go run ./cmd/openapi-gen -check`, and `pnpm --dir web test` (400/400) passed with normal Windows workspace access. A focused rerun after the root/nested Yarn-state filter change also passed. Lockfile-installed VitePress build, accessibility check, and Pages workflow check passed. |
| Hosted Docker recipe matrix | [Docker job 107865712576](https://github.com/tyhuang9/rig/actions/runs/36069131987/job/107865712576) passed in 4m49s on implementation head `1e5821d`. Its `TestLiveStandaloneRecipeMatrix` step passed with an explicit named Go test pass event; both npm/Node and Yarn/Vite subtests passed. The job also passed the inherited generated-image, Next.js, blue/green, external TLS, and exact owned-resource cleanup steps. This is an Ubuntu 24.04 image/port gate; it has no persistent plan, configuration, deployment, or release IDs. Those identities are verified in the separate controller journey. |

No real GitHub consent/archive walkthrough or production deployment is claimed.
M2 remains open until those and the complete browser/controller/database journey
are accepted on an integrated head. No database or cache is provisioned by Rig.
