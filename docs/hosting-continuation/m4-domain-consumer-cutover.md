# M4 generated-domain consumer cutover

Status: implementation in progress. The combined baseline below is verified;
the public consumer cutover is not yet accepted or activated.

## Purpose

Connect normal controller startup, generated deployment routing and retained
runtime accounting to the same generated-domain owner. This is one cohesive
change: the public factory, consumers, accounting and closure of ordinary
Manager mutation methods must be delivered together.

Frontend and backend containers remain hosted by Rig. External databases remain
application-owned and configured through scoped runtime secrets. This work adds
no managed database or Neon provisioning.

## Accepted starting point

[PR #220](https://github.com/tyhuang9/rig/pull/220), commit
`46e0885c5c850d7cfe901b4ec142c151f30eac7a`, provides the private construction
prerequisite. Its [separate record](m4-generated-domain-private-construction.md)
describes the accepted scope and evidence limitations.

The local combined baseline is commit
`34e6435617e812e876956433e0c9dd60b475d784`, tree
`313b5f55e613df897518cef29414bf7c89b96256`. Five explicit merge commits preserve
the complete reviewed histories of PRs #214, #215, #216, #218 and #219. They
combine CI partitioning and diagnostics, static-server diagnostics, and the
separate LAN and registry fixture corrections. PR #214 includes the PR #208
route-pause attribution work.

All 51 contributed files retain their exact input Git blobs: 35 code/test
files, five workflow/script files and 11 documentation files. The five inputs
add 37 paths relative to PR #220, with no conflicting paths or conflict
resolution. This is source-integrity evidence, not public-runtime acceptance.

## Required implementation boundaries

- One completed domain owns the Manager view, candidate authority, route
  authority, ledger/head family and capacity budget. Normal composition must
  not adopt the startup Manager or allocate a second budget.
- Route publication and activation require the original deployment claim,
  complete candidate and predecessor rosters, exact head/route epochs and a
  durable activation receipt. An uncertain commit cannot authorize restoration.
- Retained accounting must use the same budget as candidate replacement.
  Ordinary restart requires narrow reconstruction from exact persisted
  receipts and observed resources. Missing, crossed or ambiguous ownership
  evidence remains a sticky refusal; descriptive snapshots alone do not grant
  authority.
- Startup must finish its protected typed-recovery decision, release the
  outer effects lease before construction, then reacquire it and freshly
  reattest the same completed domain before composing workers. Construction
  owns its internal exclusions; callers cannot supply a success proof.
- Recovery-only startup continues through its existing typed path and creates
  no normal generated domain. Public ordinary `Switch`, `Provision` and
  `Recover` must become unconditional refusals only with their consumers moved
  to the domain services.
- Keep the exact SQL and receipt-label checks that prevent legacy removal of
  receipt-owned predecessors. Receipt-owned retirement remains separate work;
  a safe retirement pause must not be relabeled as successful deployment.
- Preserve the route-pause stage/cause diagnostics from PR #208 and the
  immutable history and uncertainty records used for recovery.

Cold-accounting reconstruction is a deliberate addition to the earlier cutover
map. PR #220 refuses retained generated state; wiring it directly into normal
startup would reject retained-host restart. Its empty bootstrap also acquires
the effects lease itself, so calling it while the outer startup lease is held
would contend with that lease. These findings motivate the two startup changes
above; they do not retroactively expand PR #220's acceptance.

## Combined-baseline verification

Executed on 2026-10-23 with Go 1.26.7, readonly modules, CGO disabled and explicit
Windows/Linux amd64 targets. Linux binaries ran natively through WSL. The
selected parent names were discovered before execution; a missing parent fails
the check.

| Package | Selected parent |
| --- | --- |
| generatedingress | `TestGeneratedDomainConstructionJoinsOneCompleteOwner` |
| generatedingress | `TestGeneratedDomainRegistryFencesLegacyConstructionAndTypedActivity` |
| generatedingress | `TestGatewayCurrentLANCommitRecoveryWithdrawsAfterAuthorityLoss` |
| generatedingress | `TestGatewayCurrentStateMachineGrantCommitsAndQuarantinesFailedResolution` |
| generatedexecutor | `TestGeneratedExecutorRoutePauseDiagnosticControls` |

Each platform passed five unique parents and 12 unique subcases, with 19 raw
RUN/PASS events including isolated subprocess echoes. There were no failed or
skipped tests. Test compilation, production builds and vet passed for hostd,
autodeploy, generatedexecutor, generatedimage and generatedingress on both
targets. Ten compiled test binaries are retained; only the selected ingress
and executor parents were executed.

The canonical `pwsh -NoProfile -File scripts/check-generation.ps1` passed.
The existing Bash contracts also passed:

- `scripts/check-generated-runtime-race_test.sh`: 42 selector invocations,
  three workflow matrices and 16 refusal/failure cases.
- `scripts/check-relay-outage-diagnostic-projection_test.sh`: three accepted
  and 22 rejected diagnostic projections.

The original harness omitted the task-local jq directory from its Linux PATH.
After all Go and canonical checks had passed, its first Bash step exited 127
with `jq: command not found`. The retained, checksum-verified jq 1.8.1 binary
was then used for a separate exact-source shell check. Both scripts passed;
no Go tests were rerun. The failed harness receipt remains part of the record.

All 1,071 Go/module source hashes remained unchanged through the baseline.
Aggregate diff checking passed. Previous immutable unit evidence is reused for
the independently reviewed inputs; this finite selection checks their new
interactions without claiming whole-repository acceptance.

### Local evidence identities

Evidence lives under
`temp/m3-lan-external-db-20261015/m4-domain-consumer-cutover-verification/`.

| Record | SHA-256 |
| --- | --- |
| Whole-parent integration | `b63cc6bd5bf91130294d5b52da44a205fee2adcf2bb0dd143a3d19d869062392` |
| Exact parent source union | `75f9d18f705a9b8b18d6a59ea534c422ac36987965b6d032b82c0567ce10e02e` |
| Original Go/canonical harness, including jq PATH failure | `910c20baaa487dde1173aca3cc8886499666e3baae3bb139a69b55f8edc9e8c5` |
| Corrected shell environment | `1a24a55620fa8160482dbdc09637828199329ebbc3ed34ac5a283f47ce51bd39` |
| Accepted combined baseline | `5f4b640753aac5238d8202e4d93f2dd3271f2caa646bfc71b5ca313b8c1795cb` |

## Focused activation checkpoint

On 2026-10-23, four Windows tests passed against a retained 14-file source
snapshot: two database migration tests and two route-authority tests. The
test binaries were compiled with Go 1.26.7 and the environment described above.
The selected runs used `-test.v -test.count=1` with
`-test.run=^TestGeneratedRouteActivationsMigration` and
`-test.run=^TestGeneratedRouteAuthority`. All four selected parents executed;
none failed or skipped.

The route tests exercise the physical route path through test drivers and the
original deployment claim. They verify a single receipt/head/roster commit,
refusal of copied authority and execution values, refusal of reuse, and a
changed claim after route proof causing zero SQL activation commit. These are
local controlled tests; they do not establish live Docker behavior.

Security review of the provisional interface found two issues: an exported
proof-bearing writer could accept caller-authored activation evidence, and
shallow Go value copies could duplicate the intended one-use execution. The
successor moves the writer into the private route owner, checks original
receiver identity, and shares execution liveness state. Scoped review confirms
both corrections in the tested snapshot. The original findings and separate
copy-boundary erratum remain in the evidence record.

| Record | SHA-256 |
| --- | --- |
| Focused activation receipt | `5a441a812ebe88845e5b765ff99406d5a1e6f6f36befae9a56e19a7370e1cf00` |
| Tested 14-file snapshot manifest | `ae6e8444b56e80793fe65458b7e6cdf2b8886be39ca35398ade7c47c1249709d` |
| Scoped successor security review | `e9f0d3d8e88d276ee109274201d50210152c40d0b427ad32d0ea44d3cce50a1a` |

This checkpoint does not accept the complete consumer change. Native Linux
execution, transaction failure injection, lost-acknowledgement cases,
predecessor restoration, concurrent calls and the final consumer inventory
remain required. Startup and shared-accounting implementation is still in
progress. Two preceding harness failures are retained separately; they are
not counted as successful test runs.

## Completed-join and claim checkpoint

Two further Windows parents passed on 2026-10-23:

- `TestGeneratedDomainConstructionJoinsOneCompleteOwner` holds the outer
  effects lease during the join and verifies the same owner family is reused,
  protected state bytes remain unchanged, owner registries remain empty, and
  no constructor mutation or capacity probe is dispatched. It also checks
  Docker and accounting drift refusal, successful exact rejoin after Docker
  drift is removed, and rejection of a different environment owner.
- `TestGuardRouteActivationUsesExactWaitingHealthStage` passes four subcases:
  `waiting_health` is accepted; `applying`, `preparing`, and `succeeded` are
  refused.

Both compiled test binaries ran with `-test.v -test.count=1` and an anchored
selector for the respective parent. The join's isolated subprocess repeats
its parent in the raw log; it is counted once. All parents and four subcases
passed, with no failures or skips. The retained receipt binds commands,
environment, both binaries and all raw logs:
`541d7b676c508b97c44af92c7f69d67bd24b4acf8f41d128ec540bfa423b55ae`.

Its 1,175-file before/after manifest covers Go, module and SQL sources and is
unchanged during the run. This scope differs from the Go/module-only baseline
manifest. These controls establish the bounded join and claim behavior;
retained-accounting reconstruction and normal controller composition still
require implementation and verification.

## Expanded migration checkpoint

The three selected Windows migration parents passed on 2026-10-23:

- `TestGeneratedRouteActivationsMigrationFreshSchemaAndMirror`
- `TestGeneratedRouteActivationsMigrationUpgradeHasNoHistoricalBackfill`
- `TestGeneratedRouteActivationsMigrationActivationGuards`

They cover the embedded/public schema mirror, repeated migration without
fabricating historical receipts, unchanged legacy head identity, immutable
receipts, duplicate or crossed activation refusal, and head/phase guards.
The guard test also exercises valid receipt-linked head and phase transitions.
Each rejected operation checks the retained state afterward.

The exact anchored selector was discovered and executed from a Windows test
binary; all three parents passed with no failed or skipped tests. The receipt
is `310257140e522b254a86cc5f52f02d00218c570ecc086d3368acb4b85a1a4c5f`.
It binds the binary and the changed test file's before/after hash. This focused
checkpoint does not supply a complete source manifest or replace final
cross-platform transaction and consumer verification. Earlier fixture and
harness failures remain retained in the local evidence.

## First cold-reconstruction checkpoint

The Windows `TestGeneratedDomainConstructionReconstructsColdAccounting` parent
and its `quiescent_current` subcase passed on 2026-10-23. The fixture creates,
starts and health-checks a candidate, activates it through the sealed route
operation and original claim, and finishes its runtime state before constructing
a new owner from retained evidence. The reconstructed manifest identifies the
same active deployment and component without adding a duplicate capacity charge.
The test also rejects unexpected mutation or capacity-probe commands during
this quiescent reconstruction.

The exact selector was
`-test.run=^TestGeneratedDomainConstructionReconstructsColdAccounting$/^quiescent_current$`,
with `-test.v -test.count=1 -test.timeout=90s`. One parent and one subcase ran and
passed; none failed or skipped. The receipt is
`3bc20f8800f128f08236e756dc3b31080931c588edb21e989fa203a018bbf9c5`,
the retained raw log hashes to
`979485daf6acb06df597a416992ea0f60d3173895fd23105509b0f757218c5af`,
and all 1,177 Go/module/SQL source entries were unchanged during the checkpoint.
The binary hash was independently matched to the receipt.

This controlled-driver test reaches the real constructor and ownership code;
it is not live Docker acceptance. It does not exercise acquisition for an
interrupted deployment, concurrent drift, every cold phase, or native Linux.
Implementation continues after this source snapshot, so final verification
must bind the completed consumer source separately.

A separate read-only security review found no actionable issue in the retained
18-file production snapshot. It checked exact durable/resource joins, capacity
retention and lock order, and the cold-only gateway eligibility change with its
full static, restart-config, live-config and nonrouted challenge checks. The
snapshot manifest hashes to
`eef3167ba7c52245692712c6977e46a06a4973f802656234d68ded100f6cedeb`,
and the review to
`9fb5e87328a556c3f734d908507c6f0b733f50be0c00b363c71ec27f8b491575`.
This scoped source review does not close the remaining execution tests or
accept the complete consumer integration.

## Retained-capacity and transaction-failure checkpoints

A successor Windows run passed both `quiescent_current` and `retained_risk`
under `TestGeneratedDomainConstructionReconstructsColdAccounting`. The latter
creates a real candidate through the sealed path, leaves it in the interrupted
health-waiting state, and reconstructs its exact receipt and retained capacity
charge. It also verifies one capacity probe during acquisition. One unique
parent and two unique subcases passed; the separate invocations produce four
raw RUN/PASS events with no failures or skips.

The receipt is
`a2c09e90517f1573f893c97f8fd22b1abc73d60b1175d0c389805dc757f74593`.
It corrects the original report's omission of indented child PASS lines by
parsing the preserved raw logs. The original receipt remains retained, and
this reporting correction did not rerun tests.

The same compiled binary then passed three activation parents and 11 subcases:

- `TestGeneratedRouteActivationTransactionFailuresRollBackAllSQL`: four
  reached statement failures and a deferred-constraint failure at commit;
- `TestGeneratedRouteActivationExactPersistedReplayAndCrossedFields`: a real
  sealed activation before exact replay and crossed receipt, roster, route
  proof and original-claim rejection;
- `TestGeneratedRouteExecutionIsOneShotAcrossConcurrentAndFailedOriginalUse`:
  concurrent one-use enforcement and consumption after a failed use.

The activation receipt is
`6c64daff20f6d2346d30696bd24a4010a3a7e5b15edeb3aae86b59ef66231b63`.
All 14 raw RUN/PASS events were verified, with no failures or skips. Each
checkpoint retained an unchanged 1,177-entry Go/module/SQL source manifest;
both binary hashes and all executed test logs were independently matched.

These tests do not establish an actual lost acknowledgement after a durable
commit, predecessor restoration, the complete cold-state matrix, native Linux
execution, or live Docker behavior. Those gaps remain explicit requirements
for the completed consumer change.

## Remaining acceptance and rollback boundary

At the local core checkpoint, `go build ./cmd/hostd ./internal/autodeploy
./internal/generatedexecutor ./internal/generatedimage ./internal/generatedingress`
passed for Windows and Linux amd64. Read-only `gofmt -d` was clean for all 25
changed Go files. Receipt
`489426b714e6871e9acb588fa6cb201a8b1a5bdf83d94db80877c456a5750dae`
binds the 27 held core paths and both module files across those gates. The
separate in-progress predecessor test was excluded from this commit boundary.
An additive fieldwise manifest comparison corrected a reporting-only JSON
representation mismatch; the original receipt is retained and no gate was
rerun for that correction. These builds are not Linux test execution.

Completion of implementation, the remaining failure/boundary tests, aggregate
security and source review, final caller inventory, and hosted Docker/race
verification remain outstanding for this cutover. Local Docker and race
execution are unavailable.
No live controller activation or deployment was performed to establish this
baseline. M4 is incomplete.

Before activation, the cutover can be withheld or reverted as one change while
retaining its reviewed prerequisites. After any later activated run, a source
rollback does not authorize deletion or adoption of residual resources,
discarding capacity charges, or rewriting immutable ownership evidence.

Migration 042 is additive and retains immutable activation rows. If that
migration has run, preserve it during a source rollback. Its head-update and
route-to-draining guards intentionally prevent an older binary from performing
the former split activation writes. Rolling back code therefore does not
restore the old activation behavior. Removing the schema, fabricating receipts
for historical activations, or downgrading the database is outside this
rollback procedure.
