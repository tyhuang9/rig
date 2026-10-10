# M3 startup ordering acceptance evidence

## Scope and baseline

Date: 2026-10-07 (America/Chicago).

Branch: `feature/hosting-m3-startup-ordering`, based on clean local runtime commit
`b813eff283a2590cbdabd76fd1b9044ef36d0a6f`. The approved restart-driver integration
was already present in the runtime branch; this unit does not repeat it.

Purpose: prepare retained gateway rebind recovery before ordinary deployment
admission, then inspect fresh state with the same gateway Manager before normal
controller startup.

The baseline startup selection passed 17 top-level tests and 34 subcases, with
zero failures/skips, exit 0, in 10.431s. Baseline log SHA-256:
`0F5D0D2470172BED782286673E262C254FAE701844F62CCB9152FEE7A5D1DA34`.

## Implementation and invariants

- Existing current inspection now reports active claim format separately from
  current-state version, and an advisory recovery mode derived from validated
  protected state. It still describes legacy active claims; startup refuses
  unsupported active recovery formats.
- After database open and while the existing controller-owner lease is held,
  hostd probes rebind presence and prepares retained history. This happens before
  ordinary startup admission takes its deployment-effects lease. Each recovery
  API owns/releases its own leases.
- Active typed recovery takes precedence over predecessor markers. Only
  prepared, successor-ready, and database-committed phases are dispatched.
- Stable rebound current: restore serving state, then terminal recovery/attestation.
  Completed LAN batch: restore serving state and retain its exact marker, items,
  and optional legacy pending state for the existing dedicated retirement path.
- Native, ordinary pending-route, pending-LAN, and incomplete-batch paths keep
  their existing recovery ownership. Ordinary route recovery still runs under
  startup admission during runtime composition.
- Recovery results must agree with fresh current inspection and retained terminal
  evidence. COMMIT selects the attempted operation and receipt. ABORT preserves
  the predecessor authority and protected mode/version/revision/digest, while
  allowing immutable abort history to grow. Database-committed cannot ABORT.
- After ordinary admission is acquired, startup repeats protected/SQL inspection
  against the post-preparation checkpoint. It rejects unprepared new rebind
  history or drift. This comparison precedes legitimate route recovery and batch
  retirement, which may change current state under their own existing checks.
- The same Manager proceeds through ordinary inspection, LAN recovery, and
  runtime composition. Existing emergency withdrawal and final admission checks
  remain. Errors, cancellation, unhandled restore, or invalid results prevent
  worker/HTTP serving startup. The controller TCP listener is reserved earlier;
  reservation does not start HTTP serving.

Production effect factories remain closed. This change wires orchestration; it
does not enable the concrete rebind/current-serving adapters in production.
No persisted schema/format, runtime secrets, database provisioning, or historical
receipt rewrite is introduced.

## Verification plan and commands

Go 1.26.0 on Windows. Existing module/lockfile resolution was kept read-only.
`GOCACHE=C:/Users/huang/Documents/Projects/Rig/.go-cache-m3`.
The repository has no single comprehensive read-only verification command;
affected Go checks and the all-Go production build were selected. No frontend
source changed.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=8m -json ./cmd/hostd -run '^Test(DeploymentEffectsAdmission|GatewayStartup|RuntimeComposition|LANRecoveryBatch)'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=8m -json ./cmd/hostd ./internal/generatedingress -run '^Test(Gateway(RebindStartup|Startup)|LAN(RecoveryBatch|GrantStartup|DisableStartup|AccessStartup)|DeploymentEffectsAdmission|RuntimeComposition|GeneratedCompositionRecovers|GatewayCurrentRecoveryMode|InspectGatewayRebindCurrentSeparates)'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=12m -json ./internal/generatedingress -run '^Test(GatewayRebindCurrentStartup(DistinguishesRouteAndLANRecovery|RouteRecoveryBeforeAdmission)|GatewayCurrentServingRestoreKeepsCompletedBatchAndTerminalSQL|GatewayCurrentLANRecoveryRetiresCompletedBatch|GatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists|GatewayCurrentRouteOperationProviderAbsenceIsFailClosed)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=20m -json ./internal/generatedingress -run '^TestGatewayRebindConcreteComposition(CommitsAndReplaysSameDockerState|RecoversForwardAfterDurableFailures)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd ./internal/appaccess
go build -mod=readonly -buildvcs=false ./...
```

Logs are outside the checkout under
`C:/Users/huang/Documents/Projects/Rig/temp/m3-startup-order-20261007-*.jsonl`.
Each test-output line is persisted immediately; companion `.exit` files record
process exit. Package completion and expected test discovery must both be
checked. An intermediate compile caught a test-only reference to
`input.OperationID`; it was corrected to `input.Inspection.Spec.OperationID`
before the state/composed runs. That failed compile is not acceptance evidence.

Initial focused dispatch/mode/inspection tests passed 6 top-level tests and 50
subcases across both packages, without failures/skips. The controller run before
review corrections passed 26 tests and 79 subcases without failures/skips. These
are development checkpoints; the reviewed run supersedes their startup result.

The source/test tree recorded after review fixes is
`7d5824fb611721f8f3e4f9bbca37632e8fca1ade` (before this evidence document).
Vet, all-Go build, and staged whitespace checks passed on that tree. The longer
generated-ingress gates started before the additional legacy/crossed-union
inspection test was added; their selected tests and production generated-ingress
source are unchanged. The reviewed run compiles and exercises that added test.

### Final gate results

- `reviewed`: 38 top-level tests and 109 subcases passed across two packages,
  zero failures, exit 0. Controller package: 13.331s; generated-ingress: 56.414s.
  Expected discovery included all four new hostd startup tests, the mode and
  active-format inspection tests, existing admission/emergency/LAN/runtime
  composition cases, and read-only startup presence cases. Log SHA-256:
  `2BB4FD0B677AC2A5B6F09498BE48BC310ED8B8F00722B55CE5CAFD302A235657`.
  Three existing platform cases skipped: POSIX permission preservation requires
  Linux, and symlink-parent/artifact cases could not obtain the Windows symlink
  privilege. The parent symlink test's pass does not establish its skipped cases.
- `state-regression`: all six expected top-level tests and nine subcases passed,
  zero failures/skips, exit 0, 275.440s. This includes real protected state
  classification for pending route/LAN/batch, ordinary-route recovery, completed
  batch restoration without retirement, dedicated retirement, and both closed
  provider/factory guards. Log SHA-256:
  `3707F387424C9B168E0A910A8EA8BD731E5343AA5C4F67CBA8E87EF903620E03`.
- `composed`: both expected top-level tests and five subcases passed, zero
  failures/skips, exit 0, 719.696s. Commit/replay including stopped-current
  restoration and corrupt-config withdrawal took 211.32s. Active recovery after
  a lost completed-progress acknowledgement, before database commit, and before
  terminal SQL commit took 508.04s together. The added startup inspection agrees
  with the actual selected authority before/after recovery, while the same
  retained backend checks effects and immutable SQL/protected history. Log
  SHA-256:
  `961208C4D6CF92ED4864E5793EE5E53C9F4A67655FDA6487FBFE3580F465060C`.

All three final-gate processes were reaped. Together these selected gates passed
46 top-level tests and 123 subcases, with three documented platform skips. The
separate pre-existing legacy fixture failure below remains outstanding; these
scoped passes are not a clean full-repository suite claim.

## Independent review

The existing M3 integration orchestrator and security reviewer inspected the
aggregate source and new files. They found a missing ABORT predecessor-state
comparison and an inconsistent native test fixture. Both were fixed; native and
rebound self-consistent state-drift refusals were added. Final source/security
review returned GO with no remaining source findings. Independent evidence
review verified the completed reviewed/state results, hashes, unchanged source,
and the pre-existing fixture failure, conditional only on the composed gate
and final evidence row. The now-completed composed package satisfies that gate.
Reviewers did not write files or run tests.

## Limits, rollback, and remaining acceptance

The controller dispatcher tests use bound callbacks. They cover recovery order,
failure propagation, bounded effects-lease reacquisition, and Manager reuse.
Generated-ingress tests use actual SQLite/protected files plus simulated Docker
commands, configuration, probes, and resource state. They are not a live hostd
process restart or real Docker networking test.

No live Docker, Linux race, abrupt process-death, frontend, or full repository
test-suite acceptance is claimed. Windows-specific skipped cases are listed
with the final results above. M3 remains incomplete. Production driver activation and
real Docker/process/hosted gates are still required; the prior legacy-format
continuation composition gap also remains.

The next-unit read-only audit identified a pre-existing stale fixture in
`TestGatewayRebindAttemptViewUsesTypedCheckpointAndActualPriorReceipt`.
Running it explicitly with `go test -mod=readonly -buildvcs=false -p=1 -count=1
-timeout=3m -json ./internal/generatedingress -run
'^TestGatewayRebindAttemptViewUsesTypedCheckpointAndActualPriorReceipt$'`
failed at `gateway_rebind_attempt_test.go:65` in 17.911s (exit 1). The fixture
supplies a legacy `Receipt` but no canonical `Terminal`, which the current adapter
requires. Both adapter and fixture are unchanged from this unit's baseline.
The failure is not a passing full-suite result or a demonstrated live runtime
failure. The next focused unit should repair the canonical legacy fixture and
its receipt-substitution refusal, then compose typed commit/rollback from a
retained legacy committed predecessor without altering legacy receipt bytes or
enabling legacy active recovery.
The `legacy-prerequisite` log SHA-256 is
`CF44F56653AD28E54916C22A1EB398CB30288E0471A352AA162EA4E5A8929C0D`.

Rollback is a code revert with SQL and immutable protected history retained.
An older binary may refuse retained recovery state; operators must not delete
protected history or credentials to force startup. No publication, GitHub merge,
deployment, controller restart, or external database provisioning was performed.
