# M2 reviewed deployment preconditions

**Recorded:** 2026-09-24. **Branch:** `feature/hosting-m2-deploy-preconditions`,
based on the unmerged [M2 draft PR #71](https://github.com/tyhuang9/rig/pull/71)
at `4331d626662db03cf27f7ce2e4f1eff0805a5197`. Published as
[draft PR #72](https://github.com/tyhuang9/rig/pull/72) at
`79b50039394e5a8e0e910c084f4d63f45c7bd065`. These checks establish an
integration candidate, not M2 acceptance.

## Implemented slice

- A generated latest-deployment request must supply the exact accepted plan
  revision ID/number and scoped configuration revision ID/number shown at
  review. Empty configuration ID with revision zero is explicit and valid.
  Compose latest deployment and immutable prior-release deployment keep their
  existing request paths. Older clients that submit an unpinned generated
  latest request receive `reviewed_revisions_required` instead of deploying a
  newer head silently.
- New jobs compare those heads inside the SQLite writer transaction. Stale
  reviewed requests return `reviewed_revision_stale` without a durable job.
  An exact idempotency-key replay returns its original job after head drift;
  a changed request under the same key remains a conflict. Unpinned Compose
  jobs also recheck the accepted strategy in that transaction.
- The generated worker checks reviewed heads around immutable source
  materialization before build or route effects. The runtime router cannot
  dispatch a pinned generated job to Compose after plan drift; an unpinned
  latest job cannot run through the generated executor. Recorded deployment
  provenance remains the recovery source after it is initialized.
- Authenticated job lookup by application, requesting actor, and exact
  idempotency key lets the browser recover a submitted job after a lost
  response. The response is `no-store`. The setup page preserves an uncertain
  key, announces lookup progress, keeps keyboard focus, and shows the durable
  job result. The generated **Deploy latest** action in application history
  opens reviewed setup.
- The controlled hosted-controller fixture now sends exact plan/configuration
  pins for its initial deployment, healthy same-source replacement, and
  failed bad-CA replacement. It still uses application-owned external TLS
  PostgreSQL; no database provisioning is in this slice.

## Local verification

| Check | Result |
| --- | --- |
| `go test -p 1 -count=1 -timeout=15m ./...` | Passed on exact code head `e10aad6`, including the reviewed in-flight recovery and mismatch tests. |
| `go vet ./...` | Passed. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed. |
| `go run ./cmd/openapi-gen -check` | Passed. |
| `pnpm test` in `web` | Passed: 378 tests in 14 files. |
| `pnpm typecheck` in `web` | Passed. |
| `scripts/check-embedded.ps1` | Passed: deterministic production bundle equals controller-embedded assets. |
| `pnpm e2e` in `web` with `GOFLAGS=-buildvcs=false` | Passed: 3 Chromium tests, including the real-controller dashboard journey. |
| `pnpm --dir docs check:workflow`, `build`, `check:accessibility` | Passed after this evidence page was added. |

Parallel Windows Go runs intermittently failed existing relay socket and
Compose workspace tests. Representative Compose failures reproduced on the
unchanged M2 base; the isolated relay test and serial full suite passed.
CodeRabbit could not start in WSL (`E_ACCESSDENIED`), so its automated review
is unavailable. Direct diff and security review found and closed the unpinned
generated-deploy path and both queue-to-worker strategy races. Final security
re-review found no remaining concrete issue in this slice.

## Qualification still open

The revised [hosted controller journey](https://github.com/tyhuang9/rig/actions/runs/36048506542)
passed on exact PR #72 head `79b5003`: `TestLiveControllerGeneratedDeploymentJourney`
passed in 173.68 seconds. It exercised the reviewed pins through initial,
healthy replacement, and failed bad-CA deployments; the Chromium note journey,
controller restart, external TLS PostgreSQL, and exact Docker cleanup passed.
On the same head, Documentation, Playwright browser, Relay Compose lifecycle,
Windows controller, and Generated runtime lifecycle workflows passed. The
[GitHub deployment CI run](https://github.com/tyhuang9/rig/actions/runs/36048506524)
passed its fast verification, relay packaging, and PostgreSQL/Linux race jobs.
All seven PR workflows passed on exact head `79b5003`; the race job finished
after the other six workflows.

M2 still needs a real GitHub consent and archive walk, the documented recipe
matrix and failure paths, and an attested route URL tied to the active
deployment. Windows Docker Desktop and physical LAN checks are unverified. No
merge or deployment is authorized by this record.
