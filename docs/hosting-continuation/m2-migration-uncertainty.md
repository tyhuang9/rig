# M2 approved migration and uncertain completion

**Purpose:** qualify the existing approval and durable migration boundaries
against an application-owned disposable PostgreSQL schema and a real migration
container. This is the RUN-08 slice; it is not the complete M2 exit gate.

**Candidate branch:** `feature/hosting-m2-migration-uncertainty`, based on the
unmerged reviewed-recovery head `7536c5c09351bdc6212ffe3c58638a62325e4d1a`.
The branch is published as draft PR #81 for hosted CI. Deployment and merging
require separate user authorization.

## Required behavior

- Newly detected migration evidence creates a pending approval bound to one
  accepted plan revision. An attempted deployment before approval makes no
  migration write to the external schema.
- The exact approved revision runs only its selected migration command and
  configuration keys. The application owns its database driver, schema and
  credentials; Rig does not create or manage the database.
- A compatible additive migration leaves the previous application version
  usable. The disposable schema records one append-only ledger row and counter
  increment per selected migration. Knex history makes a literal repeat a
  no-op, so the journey separately asserts that recovery never invokes the
  migration runner again.
- If the database may have committed while completion remains uncertain,
  durable recovery cannot blindly execute the same migration again. Reverting
  application code does not execute a down migration or alter that ledger.
- Test cleanup removes only exact test-owned containers, networks, images and
  disposable external fixture resources. No broad Docker cleanup is allowed.

## Verification plan

The separate migration fixture leaves the existing hosting-notes controller
journey and its TLS contract unchanged. The new hosted test crosses production
source inspection, accepted plan and approval API, scoped configuration,
generated compiler, hardened Docker migration runner, durable runtime state,
ingress and external PostgreSQL query path. A test-only wrapper around the real
migration runner ends its worker goroutine after its database side effect and
before the durable completion write. It then reopens state through the normal
recovery worker. This is controlled worker-goroutine termination, not evidence
of an OS process kill.

Run the affected Go packages, tagged live-test compile and vet, full Go suite,
vet, command builds, generation and Windows gates, fixture lockfile checks,
documentation checks and final aggregate diff review. Hosted Linux Docker and
race gates are required for actual RUN-08 acceptance. Require the exact named
Go pass event and complete owned-resource cleanup in CI; compiling the tagged
test locally is not Docker acceptance.

## Pre-change baseline on 2026-09-27

On clean base `7536c5c`, `go test -p 1 -count=1 -timeout=20m ./...` passed
with Go 1.27.0 on Windows, `GOFLAGS=-mod=readonly`, a temporary Go cache and
normal user filesystem/process permissions. The relevant migration, executor,
runtime-state, plan, jobs and hostd packages were included. No local Docker
acceptance is claimed.

## Candidate implementation

The committed fixture is a small Node application with a detected Knex
migration, an append-only run ledger, a schema counter and an application read
endpoint. The migration's `down` command throws if called. Its connection uses
a disposable root CA, explicit certificate-chain and configured-IP identity
verification. The public CA is frozen into the controlled GitHub archive; its
private key remains in the test-owned external fixture. The database URL is a
scoped sensitive runtime value, and the approved migration selects only
`DATABASE_URL`. The test-owned Docker Compose PostgreSQL instance is outside
Rig's application resource controller. Rig never provisions or cleans up a
product-owned database.

The controlled GitHub provider retains three immutable source revisions. The
mandatory hosted journey is designed to prove the following in one controller
and database lifetime:

1. Deploy database-aware v1 without a migration and verify its actual local
   route reads a counter value of zero while its table is absent.
2. Advance to v2 with new detected migration evidence. The job must pause for
   approval while v1 still serves, with both migration tables absent. Approve
   the exact v2 plan revision, resume the same job, and verify a successful
   real migration, one ledger row and counter value one through v2's route.
3. Deploy the pinned v1 release using its original configuration. Its
   database-aware route must return counter value one, no migration runner call
   occurs, and the external ledger/counter remain one. This is an application
   code recovery, with no down migration.
4. Advance to v3 with distinct additive evidence. Before its approval, verify
   no second write. After approval, the real Docker runner must commit a
   second ledger row and counter increment; the test-only worker termination
   occurs before completion is persisted. Reopen durable controller state with
   the normal recovery worker and require an interrupted job, failed deployment
   with the daemon-restarted diagnostic, zero new migration-runner calls,
   unchanged PostgreSQL values, and the retained database-aware v1 route.

The same hosted workflow requires exact named Go pass events for the provider
revision contract and the live journey, followed by an always-run check that
no Rig-managed or fixture-owned Docker resources remain. It does not skip the
Docker test when the daemon is unavailable.

## Local verification on 2026-09-27

The implementation through `3cf0bbc` was checked with Go 1.27.0 on Windows,
`GOFLAGS=-mod=readonly` and a temporary Go cache. Full suite and build checks
used normal filesystem/process permissions; command builds used process-only
Git trust for this exact checkout.

| Command | Result |
| --- | --- |
| `go test -p 1 -count=1 ./cmd/hostd -run '^TestHostingMigrationFixtureAnalyzerContract$'` | Passed: fixture analysis detects the selected Knex command, evidence and `DATABASE_URL` |
| `go test -p 1 -count=1 -tags live_docker ./cmd/hostd -run '^(TestControllerJourneyGitHubProviderRetainsImmutableRevisions\|TestGeneratedCompositionMigrationRunnerFactory)$'` | Passed: frozen provider revisions and optional runner seam |
| `go test -p 1 -count=1 -tags live_docker ./cmd/hostd -run '^$'` and `go vet -tags live_docker ./cmd/hostd` | Passed: live suite compiles and vets, without executing Docker |
| `go test -p 1 -count=1 -timeout=20m ./...` and `go vet ./...` | Passed: full Go suite and vet |
| `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed |
| `pwsh -NoProfile -File scripts/check-generation.ps1` and `scripts/check-windows-controller.ps1` | Passed: generated contracts, migration mirrors and Windows protection gate |
| `npm --prefix examples/hosting-migration ci --ignore-scripts` | Passed from the committed lockfile; npm reported zero audited vulnerabilities |
| `pnpm --dir docs build`, `check:accessibility`, `check:workflow` | Passed |

The recovery-worker correction and per-SHA archive accounting were additionally
checked locally with `go test -p 1 -count=1 -tags live_docker ./cmd/hostd -run
'^(TestControllerJourneyGitHubProviderRetainsImmutableRevisions\|TestGeneratedCompositionMigrationRunnerFactory\|TestHostingMigrationFixtureAnalyzerContract)$'`,
`go vet -tags live_docker ./cmd/hostd`, `go test -p 1 -count=1 -tags
live_docker ./cmd/hostd -run '^$'`, `git diff --check`, and `node --check
examples/hosting-migration/src/server.js`; all passed. These tagged checks
compile the Docker journey but do not execute it.

At corrected head `0acfa87`, the full `go test -p 1 -count=1
-timeout=20m ./...` suite, `go vet ./...`, four-command `go build -trimpath`,
generation script, Windows controller script, and documentation build,
accessibility and workflow checks were rerun and passed. The documentation
build required normal access to ignored pnpm dependency files; its sandboxed
attempt could not read a Vite package through a local junction.

An earlier offline-only `npm ci` attempt failed because the local cache lacked
one package. The normal lockfile install passed. The docs build initially
found an incomplete ignored dependency link in this checkout; reinstalling
from its frozen lockfile with normal pnpm store access resolved it. No tracked
dependency or lockfile was changed by either local install.

The Windows host has no usable local Docker gate and CGO is disabled, so live
Docker and local Go race execution remain unrun. The first hosted workflow
result is recorded below. The existing hosting-notes journey establishes
external TLS access only on its own published head; that evidence is not
transferred to this candidate. The Compose PostgreSQL fixture is controlled by
the test harness and demonstrates Rig's non-provisioning boundary, rather than
an independently administered external database. Live user GitHub consent,
actual OS process-kill behavior and other M2 cases remain separate.

## First hosted run and correction on 2026-09-28

Draft PR #81 ran the hosted migration workflow at `982e123` (run
`36374444867`, job `108777281176`). The controlled GitHub revision contract
passed. The live journey reached the approved v2 deployment, which failed with
the sanitized `apply_failed` job code before its success assertion. Cleanup
then reported that the external fixture network and two volumes remained; the
always-run resource gate also failed. This run is failed acceptance evidence,
not a pass for RUN-08.

The pinned Knex 3.1 CLI searches for `knexfile.js` and other supported
extensions but not `knexfile.cjs`. A local invocation of the inferred
`knex migrate:latest` command reproduced the missing-configuration failure
with the original filename. With the file renamed to `knexfile.js`, the same
synthetic invocation loaded the config and reached the expected absent local
test certificate. This strongly identifies the v2 failure cause, though the
hosted log exposes only the safe `apply_failed` code. A local fixture contract
now requires the discoverable filename. The Compose teardown also omitted the
required fixture environment supplied during `up`, so its `down` could not
resolve the same project configuration. Teardown now passes that environment
without logging its values. The corrected hosted result is recorded below.

After the correction, the focused tagged Go fixture/provider/composition tests,
tagged live-test compile and vet, `node --check` for the Knex config and app,
full `go test -p 1 -count=1 -timeout=20m ./...`, `go vet ./...`, four-command
Go build, generation and Windows controller scripts, and documentation build,
accessibility and workflow checks passed locally. The Windows host still did
not execute the Docker migration or Linux race test.

## Corrected hosted acceptance on 2026-09-28

At source head `bd66acf098089b6154eb58a6b41d345b15ae7014`, the
[hosted migration job](https://github.com/tyhuang9/rig/actions/runs/36376529012/job/108783414779)
passed. Its JSON events contain one pass and no fail/skip for
`TestControllerJourneyGitHubProviderRetainsImmutableRevisions` and one pass
and no fail/skip for
`TestLiveGeneratedMigrationApprovalAndUncertaintyJourney` (135.37 seconds).
The always-run `Require complete owned Docker cleanup` step passed at the same
head, including the external fixture network and volumes that remained on the
first run.

The [PostgreSQL and Linux race job](https://github.com/tyhuang9/rig/actions/runs/36376528993/job/108783414589)
also passed at that exact head. Its PostgreSQL integration race, relay outage
race, and repository-wide race steps all succeeded. The other required PR #81
hosted controller, blue-green Docker, Chromium, Windows, documentation, fast,
generated-runtime race, relay Compose, and packaging checks succeeded;
documentation deployment was skipped by the pull-request gate. This qualifies
RUN-08 against the controlled Docker fixture. It does not establish behavior
against an independently administered database, live GitHub consent, or an OS
process kill during migration.

## Rollback

The production change is limited to an optional internal test seam
around the existing migration runner; with no test factory, production uses
the original runner. Remove that seam and the new fixture/gate to restore the
previous binary behavior. In an actual deployment, an application code rollback
must leave the external database schema and data untouched; operators need a
separate application-owned forward recovery plan for incompatible migrations.
