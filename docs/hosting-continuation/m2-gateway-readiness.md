# M2 gateway reachability candidate

**Purpose:** reject a generated candidate that passes Docker health but cannot
be reached from the gateway, while preserving the previous serving deployment.

**Branch:** `feature/hosting-m2-gateway-readiness`, based on unmerged
[recipe-matrix draft PR #75](https://github.com/tyhuang9/rig/pull/75) at
`bf782685aed81e3d63ffe8e1c3c625622795237f`.
Implementation commit: `cbea5267b718e353da3f5be288b947a498c6454d`.
This candidate was local at initial verification. It was subsequently published
as [draft PR #77](https://github.com/tyhuang9/rig/pull/77) at
`26a2837a278d120d961fae58391b605c0b290226`. M2 remains open.

## Behavior and invariants

Before loading a proposed Caddy configuration, Rig connects its already-owned
gateway to the candidate's application network and probes every endpoint from
inside the attested Caddy container. The fixed HTTP HEAD command targets the
validated, network-qualified component alias at its internal port. It uses
the immutable Caddy container ID, disables curl configuration, proxies and
redirect following, discards the body, and retains only a bounded status code.
Caddy's pinned image, running state, hardening, listeners and network
attachments are reattested before and after the probes.

The request has a two-second curl deadline and a four-second process deadline.
An exact HTTP status from 200 through 599 establishes transport reachability;
the existing configured Docker probe establishes application health. A root
404 or 405 is therefore accepted. Applications must answer HEAD `/` within
the deadline, even when their health path differs. This is a compatibility
requirement and adds a bounded probe to active-route observations as well.

A proven failure records `gateway_readiness_failed` in public deployment and
job history, compensates temporary attachments/configuration through the
existing bounded rollback, and keeps the previous active head. Observation
withholds a verified URL for an unreachable route. Cancellation remains
cancellation. If compensation cannot establish safety, the existing route
reconciliation pause and candidate-retention behavior take precedence.

No application environment, credential, response body or raw Docker error is
included in the public diagnostic. External database ownership, scoped secret
delivery, runtime hardening and network isolation are unchanged.

## Acceptance coverage prepared for hosted CI

`TestLiveGeneratedGatewayReadiness` uses real runtime and ingress engines on a
disposable local Linux Docker daemon. It requires a clean preflight, then:

1. Serves and attests a correctly bound blue candidate through Caddy.
2. Starts a loopback-only green candidate and proves Docker health succeeds.
3. Requires gateway failure, the original committed route and serving response,
   and the original attested URL.
4. Removes that exact candidate and verifies a correctly bound replacement.
5. Rejects a first deployment with no committed route or verified URL and
   restores the gateway's network attachments.

The lifecycle workflow requires the exact named Go test pass event; an absent
or skipped test cannot satisfy its gate. Cleanup checks ownership and removes
only the test's preflight-checked identities. Builder configuration is separate
from the intentionally empty runtime Docker configuration.

The continuous `TestLiveControllerGeneratedDeploymentJourney` adds a fourth
same-source deployment after its successful configuration replacement and
bad-CA failure. It restores the valid CA and sets the application-owned
`API_BIND_ADDRESS=127.0.0.1`. Assertions tie Docker health to the failed
candidate's application, deployment and release IDs, and the failed fixed
gateway probe to its qualified alias. The job and public deployment must retain
the specific diagnostic and immutable plan/configuration/release pins. The
previous deployment must retain its head, attested route, API note and browser
note. These are executable assertions, not completed local Docker evidence.

## Verification on 2026-09-27

Local checks used Go 1.27.0 on Windows, the existing dependency lockfiles,
and normal user filesystem access. Hosted CI pins Go 1.26.7 and Linux Docker.

| Check | Result |
| --- | --- |
| Pre-change affected-package baseline | `go test -p 1 -count=1 ./internal/generatedingress ./internal/generatedruntime ./internal/generatedexecutor ./cmd/hostd` passed on base `bf782685`. |
| Focused ingress/runtime/executor/state/deployment tests | `go test -p 1 -count=1 ./internal/generatedingress ./internal/generatedruntime ./internal/generatedexecutor ./internal/generatedruntimestate ./internal/deployments` passed. Covers refusal, self-deadline, malformed/truncated status, accepted 404/405, all endpoints, cancellation, uncertain compensation, committed-candidate retention, post-probe drift and URL withholding. |
| Durable job diagnostics | `go test -p 1 -count=1 -run 'Test(RealDeploymentCompletionAndFailuresAreCentrallySanitized|GatewayReadinessFailurePersistsOnlyTheSafeJobDiagnostic)$' ./internal/jobs` passed. Real SQLite worker/job history retains the safe diagnostic without untrusted details. Public deployment taxonomy also passed against SQLite. |
| Full Go suite | `go test -p 1 -count=1 -timeout=15m ./...` passed. Optional live tests are not acceptance passes in this command. |
| Vet and command builds | `go vet ./...` and `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` passed. |
| Generated contract and migration mirrors | `scripts/check-generation.ps1` passed, including OpenAPI generation and registered-route contract checks. No migration or generated runtime-state repository diff exists against the base. |
| Docker harness compilation | `go test -p 1 -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` and `go vet -tags live_docker ./cmd/hostd ./internal/generatedingress` passed; no Docker execution is claimed. |
| Committed controller source boundary | `go test -p 1 -tags live_docker -count=1 ./cmd/hostd -run '^TestControllerJourneyStageSource$'` passed after implementation commit `cbea526`. |
| Application-owned fixture | `node --check examples/hosting-notes/api/src/server.js` and `pnpm --dir examples/hosting-notes test` passed (13 tests). |
| Documentation | VitePress build, `node scripts/check-accessibility.mjs`, and `node scripts/check-pages-workflow.mjs` passed with the new evidence page. |
| Formatting and patch | `gofmt -l` on changed Go files produced no output; `git diff --check` passed. |

The first attempted runtime-state diagnostic extension failed a real SQLite
test: immutable migration 020 constrains its diagnostic values. The final code
keeps the existing internal `route_switch_failed` value and exposes the more
specific public job/deployment diagnostic. No historical migration or table
was rewritten. Review also corrected self-timeout classification, full
post-probe attestation, builder/runtime configuration separation, and exact
failed-candidate evidence attribution before the implementation commit.

## Limits, rollback and next gate

Production and aggregate security reviews have no unresolved material findings
for local commit. Unit contracts and local builds are verified; real Docker,
the extended controller/database/browser journey, and race tests are unrun for
this head. Windows has no Docker CLI and CGO is disabled. Earlier green PR #75
runs establish only that earlier head; they do not qualify this change.

Rollback can revert this implementation or use the previously qualified
binary. No schema rollback is required. Code inspection confirms older readers
accept the terminal public diagnostic strings; an older-binary rollback has
not been executed. Reverting removes future gateway-readiness
enforcement and its specific diagnostic. Existing deployment history and
active routes must remain intact.

Publication of draft PR #77 was explicitly authorized. Its required hosted
checks passed at the exact PR head above, including the controller/Docker,
generated-runtime race, PostgreSQL/Linux race, Chromium, Windows, and fast
verification jobs. Documentation deployment was skipped by its expected PR
gate; CodeRabbit reported success because it skipped source review on the
draft. These results qualify that source branch, not a combined M2 revision.
The separate live GitHub authorization/archive walkthrough and M2 integration
remain open. No merge or production deployment is authorized by these results.
