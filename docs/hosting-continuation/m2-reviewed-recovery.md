# M2 reviewed deployment and capacity-pause recovery

**Purpose:** preserve safe generated recovery for current reviewed deployment
inputs and intentional capacity pauses without changing immutable input or
weakening runtime checks.

**Candidate branch:** `feature/hosting-m2-reviewed-recovery`, local and unpublished,
dependent on the retained capacity candidate
`62bbc419085eff40fafcf7ba7ed5b97d34951f07`.
**Verified implementation:** `9e932a180ef23df024b31d05c9610251ec153dcb`.
This page records local acceptance and prior approved-head CI evidence. It is
not a RUN-10 pass or an M2 exit claim.

## Baseline and required invariants

The base passed the full Go suite, vet, command builds, generated contracts and
migration mirrors, Windows protection gate, frontend checks and embedded asset
verification on 2026-09-27. The capacity evidence page records those results.

- Recognize exactly the deployment-input forms admitted by the jobs service.
  Retain unknown-field and trailing-data rejection and legacy release inputs.
- Match reviewed pins to persisted actual deployment provenance, never to a
  newer current head. Zero configuration remains a valid paired empty/zero
  identity. Nonzero configuration must be scoped and bound to the same plan.
- Preserve the input JSON bytes, attempt, release/deployment/plan/configuration
  IDs, runtime state and serving head when recovery is safe.
- Preserve an exact intentional capacity pause without queuing it automatically.
  Capacity can pause recovered work after admission at safe build, migration,
  candidate, health and route-switch phases. Existing runtime validators still
  decide whether those phases are safe to replay.
- Continue failing closed for uncertain migration execution, invalid artifacts
  or container identities, incompatible heads, mismatched pause fields or
  configuration scope, and malformed/mixed input.
- No schema migration, history rewrite, operator bypass or new external side
  effect is required.

## Verification plan

First reproduce reviewed-input and capacity-pause recovery failures using the
existing real SQLite recovery fixtures. Then implement strict decoding and
binding guards and run the regression and rejection matrices. Exercise public
deployment/job recovery in its startup order and repeat it to check idempotency.

Extend the continuous hosted controller journey to stop and reopen its durable
controller while the replacement is capacity-paused. Keep controlled pressure
armed, retain serving traffic and notes, then resume the same accepted job with
unchanged input/pins. Require owned cleanup and the existing exact named live
pass event.

Run affected package tests, the full Go suite, vet, command builds, generated
contracts and migration mirrors, tagged test compilation/vet and final diff
review. Actual Docker and Linux race qualification remain hosted gates here.
Paused restart is one boundary; it does not establish actual interruptions
during build, migration, candidate, route commit, drain or finalization.

## Executable reproduction on 2026-09-27

Before production changes, the following two regressions failed at their
recovery assertions on base `62bbc41`:

```text
go test -p 1 ./internal/generatedrecovery -run '^TestRecoveryPreserves(StrictReviewedCurrentInput|CapacityPauseWithStrictReviewedCurrentInput)$' -count=1
```

Both had valid scoped configuration and immutable provenance initialized before
runtime state began. The runtime was in `building`; the second job had the exact
capacity pause phase/disposition. Public deployment/job recovery unexpectedly
terminalized the runtime as `failed` with `daemon_restarted`. SQLite triggers
remained enabled. The tests did not fail on fixture creation.

The code audit separately identified the recovery decoder's missing reviewed
fields and the waiting-user query's route-only allowlist. The combined paused
reviewed-input regression exercises both guards. Existing legacy fixtures had
not exposed this current input shape.

## Approved draft CI evidence on 2026-09-27

The user authorized publication of the bootstrap, gateway-readiness and capacity
candidates as drafts targeting the recipe-matrix branch. Those are PRs
[76](https://github.com/tyhuang9/rig/pull/76),
[77](https://github.com/tyhuang9/rig/pull/77) and
[78](https://github.com/tyhuang9/rig/pull/78). This recovery branch remains local;
that publication approval does not include it or any merge/deployment.

| Candidate | Completed continuous controller Docker evidence |
| --- | --- |
| Gateway `26a2837a278d120d961fae58391b605c0b290226` | [Run 36348139064/job 108701413471](https://github.com/tyhuang9/rig/actions/runs/36348139064/job/108701413471): exact `TestLiveControllerGeneratedDeploymentJourney` pass, 206.47s; cleanup step passed |
| Capacity `62bbc419085eff40fafcf7ba7ed5b97d34951f07` | [Run 36348160571/job 108701474022](https://github.com/tyhuang9/rig/actions/runs/36348160571/job/108701474022): exact same named pass, 179.09s; cleanup step passed |
| Gateway dedicated ingress gate at `26a2837` | [Run 36348139113/job 108701413861](https://github.com/tyhuang9/rig/actions/runs/36348139113/job/108701413861): exact `TestLiveGeneratedGatewayReadiness` pass, 43.42s |

The parent parsed completed logs and required exactly one named Go `pass` event
for each journey, without printing application output. The capacity candidate
therefore establishes its controlled-pressure, retained-serving, authenticated
same-job resume contract in real Docker with the external TLS database and
browser. It does not include the newer paused-controller-restart extension on
this branch. A fresh check confirmed all verification gates passed for each
approved head: full PostgreSQL/Linux race, generated runtime race, Windows,
fast, Chromium, relay packaging/lifecycle, Docker and documentation build.
The final PostgreSQL/Linux race jobs were
[bootstrap 108701360088](https://github.com/tyhuang9/rig/actions/runs/36348120162/job/108701360088),
[gateway 108701413698](https://github.com/tyhuang9/rig/actions/runs/36348139055/job/108701413698), and
[capacity 108701474064](https://github.com/tyhuang9/rig/actions/runs/36348160540/job/108701474064).
Documentation deployment was skipped by the PR gate. These results do not
qualify this newer local recovery head or complete M2.

## Implementation and review

Recovery now recognizes the same six durable fields and input forms as the jobs
service. Reviewed input is permitted only with current configuration mode, no
release override, valid plan identity/positive number and a valid configuration
pair. Legacy release input remains supported. Unknown keys, trailing JSON and
malformed or mixed identities remain rejected.

The query loads actual configuration through its exact application/ID/number
and requires scoped format 2 bound to the persisted deployment plan. Reviewed
pins are compared with that actual provenance, without reading current plan or
configuration heads. Intentional capacity pauses are recognized only at the
safe phases where the executor can perform admission; existing artifact,
container, slot, head and migration-state validation still applies. No recovery
operation changes accepted input bytes or queues an intentional user pause.

The live journey reuses one concrete controller reopen/composition/worker/router
helper for both restarts. It stops and joins the old worker before closing the
database, keeps pressure armed through the reopen and updates its delegate to
the new real ingress manager. The same counters and provider survive the
restart. Exact job/input/attempt, component identities, deployment/release pins,
serving head, owned resource IDs and route/API/browser notes are checked before
capacity is released. It resumes the original job through the authenticated
endpoint and retains the healthy replacement and subsequent unhealthy case.

Read-only security, code and final integration review found no material defect.
Review identified two missing contract tests; both were added and verified:
a valid same-application format-2 configuration belonging to another accepted
plan must fail closed, and a capacity pause after migration succeeds must
survive repeated recovery unchanged. The fixture creates provenance before
immutable deployment initialization; no trigger or validator was relaxed.

## Local acceptance on 2026-09-27

Verification used Go 1.27.0 on Windows, existing dependencies,
`GOFLAGS=-mod=readonly`, and a temporary Go cache. Verification ran with normal
Windows filesystem/process permissions because the sandbox restricts protected
fixtures. Command builds used process-only Git trust for this exact checkout;
VCS stamping was retained. Source and tests were held stable during the gates.

| Command | Result |
| --- | --- |
| `go test -p 1 -count=1 ./internal/generatedrecovery` | Passed, 6.715s after the final two review regressions |
| `go test -p 1 -count=1 ./internal/generatedexecutor ./internal/jobs ./cmd/hostd` | Passed |
| `go test -p 1 -count=1 -timeout=20m ./...` | Passed; recovery package 5.848s in the full run |
| `go vet ./...` | Passed |
| `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed; generated contracts, route contract and immutable migration mirrors |
| `pwsh -NoProfile -File scripts/check-windows-controller.ps1` | Passed; required named DPAPI, session, secure temporary storage, descendant-process and release-path tests executed |
| `go test -p 1 -count=1 -tags live_docker ./cmd/hostd -run '^$'` | Passed; compilation only, reported by the implementing agent on the stable source |
| `go vet -tags live_docker ./cmd/hostd` | Passed on the stable source |
| `gofmt -l` for the four changed Go files; `git diff --check` and staged aggregate diff check | Passed; no unformatted files |
| `pnpm --dir docs build` | Passed |
| `pnpm --dir docs check:accessibility` | Passed |
| `pnpm --dir docs check:workflow` | Passed; publication contract retained |

The real SQLite matrix covers reviewed and legacy current/original input,
zero configuration, current-head drift, safe active continuation phases,
repeated exact capacity pauses through migration pending/succeeded, candidate,
health and route switch. Failure coverage includes unknown/trailing/malformed
JSON, mixed input, invalid IDs and pairs, mismatched numbers or pins, old
configuration format, another accepted configuration plan, missing migration
approval, unsafe pause phases, mismatched disposition and uncertain migration.
It checks public deployment/job startup order, exact accepted input bytes and
attempts, terminal diagnostics or retained pause, and restart-event behavior.

No frontend or embedded-asset source changed. Their full test/typecheck/build
and embedded hash checks passed on the retained capacity base, as recorded in
the capacity evidence. Documentation build, accessibility and workflow checks
passed before the separate evidence commit.

Local race execution is unavailable because CGO is disabled; enabling an
unprepared toolchain was not attempted. The earlier approved heads passed
hosted Linux race and PostgreSQL gates, but that evidence does not apply to
this newer source revision. Actual Docker paused restart, its named pass event
and owned cleanup remain unrun for this candidate. Live user GitHub setup and
the other outstanding M2 acceptance boundaries remain incomplete.

## Rollback

Reverting the code needs no schema rollback. An older binary restores the
previous recovery failures for outstanding reviewed jobs and capacity pauses.
Complete or safely cancel affected jobs before downgrading, and preserve the
data root, immutable history and owned serving resources. Reverting must not be
described as transparent recovery of those in-flight jobs.
