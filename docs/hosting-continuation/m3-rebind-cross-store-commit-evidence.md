# M3 cross-store commit and recovery

Status: implementation plan accepted after source and security review; clean
baseline verified. Database and runtime implementation is underway. No publication, merge or
deployment is authorized for this branch.

Branch: `feature/hosting-m3-rebind-cross-store-commit`.
Base: `a888cac5f34c46aad74afd5c3a0392eec914c509`, the reviewed private handover
candidate. That handover's draft-publication request is pending. Its prerequisite
[draft PR #136](https://github.com/tyhuang9/rig/pull/136) was initially published at
`bdbb697f767da4e71664da1434a75e6daec4b732`; its corrected current head is
`9694b4ecf0086174a4002098a2aa203c96907d50`.

## Purpose and required end state

Connect exact protected handover evidence to atomic SQL commit and recoverable
effective profile state, preserving immutable raw grants and history. The
runtime delivery plan must cover commit, phase-aware recovery, transfer-aware
readers, final physical reattestation and guarded fence release together. The
administrator API/UI remains a separate product unit. This document does not
claim that a database-committed-only foundation completes that end state.

The authoritative [cutover contract](./m3-rebind-cutover-contract.md) remains
in force. Source review established the implementation contract before
source changes. In particular, migration 034 requires the intermediate
`successor_ready` event before `database_committed`; both must be retained.

### Source-based design decisions

Migration 034 already reconstructs the original claim table with a nullable
upgrade predecessor and an exclusive typed source: either a real upgrade
operation, or a committed prior rebind plus its terminal receipt. Its exact
predecessor trigger already validates both alternatives. Therefore migration
035 need not reconstruct that table merely to admit a second rebind. It must
preserve the existing union and add explicit spec/chain-version constraints;
Go readers must handle a nullable upgrade ID rather than substitute a root
upgrade for the actual predecessor.

The existing Go and protected formats still require explicit versioned
extensions for this typed predecessor and transfer chain. Preserve their v1
canonical types and bytes. Planned resolver outputs keep immutable raw
allocation/access/grant/profile proof separate from effective current profile,
typed gateway source, transfer chain tip and receipt proof. Phase authorization
is a distinct decision, not a side effect of resolving that projection.

The pinned SQLite driver's public scalar-function API does not expose a
connection identity. Source and security review support a smaller design using
a private one-use 256-bit nonce and a nondeterministic SQL guard function:

1. Pin the connection and begin the immediate transaction before minting the
   nonce, bound to actual operation, expected state/sequence and canonical
   command fields.
2. The command's BEFORE INSERT guard compares and consumes that nonce
   atomically, before it can become a visible row. Reject wrong fields, absent
   authority and replay. A command digest supplied by SQL alone is insufficient.
3. The command's AFTER INSERT trigger performs the complete mutation program;
   every profile/head/transfer/event/claim exception requires that exact fresh
   command and expected current sequence. Retained commands cannot authorize
   later writes.
4. Require exact durable readback before commit. Cancellation, rollback or
   uncertain commit never revives the nonce; recovery mints fresh authority
   only after new complete proof.

This was the initial design decision. The guarded SQL implementation and
adversarial test evidence are recorded below; the complete runtime commit,
recovery and physical acceptance work remains in progress.
The control DB currently uses a plain file DSN with private cache. Bound nonce
values must never be logged or traced. The ordinary-SQL threat boundary excludes
process-memory access, counterfeit registered functions and arbitrary schema
ownership; raw external SQLite lacks the guard function and fails closed.
No unsafe driver access, driver fork or connection-hook wrapper is required.

## Invariants and verification plan

### Implementation order and ownership

This is one runtime delivery branch with coordinated commits, not a set of
separate prerequisite PRs. Parent owns documentation, CI/live acceptance and
the existing controller URL adapters. Database/app-access and generated-ingress
work use separate worktrees and disjoint write areas.

1. Database/app-access worker establishes the shared contract and v1 golden
   compatibility tests before dependent runtime implementation begins:

   | Shared type | Contract |
   | --- | --- |
   | `GatewayCurrentLineageRef` | Stable source kind/operation, profile identity, protected generation/identity; real prior terminal receipt for a rebind source |
   | `GatewayRebindSourceRef` | Stable lineage plus the exact frozen operational-state version, revision, digest and create-only admission checkpoint |
   | `GatewayRebindSpecV2` | Typed predecessor, explicit successor protected generation, successor/configure identity and versioned complete roster digest/count; v1 type and digests remain unchanged |
   | `GatewayRebindRosterEntryV2` | Immutable allocation/access/grant/runtime fields and nullable preceding transfer digest |
   | `GatewayRebindAllocationTransfer` | Ordered source binding and roster evidence, predecessor transfer, successor profile and receipt, canonical transfer digest |
   | `GatewayBindingResolution` | Separate raw source proof and effective current profile/source/chain-tip/receipt |
   | `GatewayRebindRecoverySnapshot` | Validated retained history, at most one active claim, exact head/commands/events/transfers, phase and recovery direction |
   | `GatewayRebindTransitionProof` | Purpose/version, operation/request/spec identities, expected SQL state/sequence/head, protected receipt/progress evidence, disposition, ordered transfers and release attestation |

2. Database/app-access worker owns migration 035 and its mirror, the spent
   nonce guard, guarded admission and command writers, complete transfers,
   resolver and phase-aware snapshots. It integrates all existing grant,
   disable, operator, upgrade and startup readers that currently equate the
   raw profile with the current head. Preserve the strict v1 prepared snapshot
   used in existing protected digests.
3. Runtime worker owns versioned protected current lineage and successor
   formats, multi-generation scanning, private admission/commit/recovery,
   route/grant/disable/redeploy integration, and controller-process startup.
   Original upgrades and real prior rebinds remain distinct; no synthetic
   upgrade claim, journal or approval may stand in for a rebind. Finalize the
   protected current-state serialization at the first architectural checkpoint.
4. Commit `successor_ready` and `database_committed` through the existing
   sequenced state machine. Successor profile/head and complete transfers
   become atomic with the latter. After new physical/protected/SQL proof, the
   final short transaction records `committed` and releases only the exact
   fence. After any historical database commit, recovery is roll-forward only.
5. Integrate normal operations after release, repeated rebinds, native grants
   introduced between rebinds, disable/redeploy, restart and old-NIC return.
   Parent updates the existing controller URL projection to use validated
   effective profile data and withhold URLs during recovery.
6. Review the guard/schema adversarially, then the complete aggregate. Finish
   QA, exact named tests, full verification, physical acceptance and final
   integration review before requesting publication of this branch.

The repository SQL writer consumes a typed proof built by generated-ingress
after fresh protected/physical observation and independently validates all SQL
bindings. Shared types carry identities/digests/manifests, with no
`appaccess` import of `generatedingress`. Historical terminal validation must
not require every retained operation's old runtime heads to be current today.

Normal route changes must not invalidate an earlier terminal receipt. The
protected operational state is generation/operation scoped: its route revision,
applications and pending recovery state can change while its origin, profile,
network and lineage identity remain fixed. A new admission retains a create-only
checkpoint of that exact current state after its prepared SQL claim commits,
before protected intent or physical effects. Historical claims retain their own
checkpoints. Original v1 protected bytes remain unchanged.

### Required invariants and tests

- Preserve prior migrations, upgrade/rebind records, raw allocations/grants,
  protected artifacts and existing digest versions. A new predecessor format
  must be explicit and may not reinterpret an upgrade operation as a rebind.
- Commit the complete roster, successor profile/head, exact receipt binding and
  sequenced SQL transition atomically. Reject direct SQL bypass, stale heads,
  partial rosters, duplicate/gapped transfers and forged receipt authority.
- Retain deployment-effects, Manager and gateway lock order. Never keep a SQL
  write transaction open across Docker, network or HTTP effects.
- Resolve raw immutable grant proof separately from effective profile/transfer
  proof. Startup, grant, disable, route validation and later rebinds must use
  one consistent chain validation contract.
- A historical database-committed event forbids rollback, including recovery
  through `unresolved`. Replay must prove exact durable state and avoid repeated
  effects. Ambiguity keeps the operation fenced.
- Fence release requires fresh SQL, protected history, successor serving and
  predecessor-withdrawal proof; later failures latch recovery-only behavior.
- Prove two sequential rebinds, rollback followed by rebind, changed runtime
  heads, interrupted transactions and lost acknowledgments, process restart,
  missing/gapped transfers and return of the old interface. Physical claims
  need actual Docker/second-device evidence, not a fake-driver assertion.

Before implementation, run the existing database/app-access suites in this
isolated worktree. During implementation, require meaningful migration,
direct-SQL, resolver, writer and failure-boundary regressions. Use the existing
Go toolchain and lockfile, repository-wide vet/build, relevant protected-history
and controller suites, tagged Docker compilation, docs checks and the repository
CI gates. Exact selected test names must be discovered and their passes checked.
Independent security, QA and final integration review are required for the
cohesive aggregate before any publication request.

## Initial baseline

The worktree was created clean at the exact handover candidate. The inherited
full-suite checkpoint at `c2d3daf` passed 48 packages and 2,048 top-level tests;
separate subsequent autosave checks are recorded in the
[handover evidence](./m3-rebind-final-handover-evidence.md). Those are inherited
checkpoints and are not evidence of this unimplemented SQL change.

The new worktree baseline completed successfully with normal Windows access:

```text
go test -mod=readonly -p=1 -json -count=1 -timeout=12m ./internal/database ./internal/appaccess
```

All 139 top-level tests passed with zero failures or skips. Database completed
in 8.956s and app-access in 39.636s. No source files had changed during this run.
The locked offline docs install reused 129 cached packages in 2s using the
declared pnpm 11.22.0 toolchain; no dependency or lockfile changed.

The new controller regression fixture then established the existing status
contract before the effective-profile adapter changes. The selected command was:

```text
go test -mod=readonly -json -count=1 -timeout=5m -run '^TestLANAccess(VerifiedStatus|StatusWithholdsURL)' ./internal/controller
```

All three explicitly discovered top-level tests and nine failure subcases
passed, with zero failures or skips; the package reported 4.324s. These tests
cover a verified native LAN URL with unchanged raw grant request, withholding
the URL for five missing/changed serving-evidence conditions, and withholding it
for four changed authorization conditions. Test-fixture construction initially
needed the existing action constant and a logger; those errors were corrected
before this recorded passing run. No production regression or completed
cross-store behavior is claimed by this fixture-only checkpoint.

After these additions, the docs workflow contract and production build passed
with normal Windows access (VitePress 4.72s). The initial sandboxed build could
not resolve an existing pnpm dependency link; rerunning with normal access
passed without installing or changing dependencies.

## Implementation and acceptance checkpoints

### Local contract and controller integration checkpoint

Shared contract commit `c04a98ea9f8e59144dcbc70707a61fb6a55091a6` was integrated
additively. Its three named canonical-format tests passed independently in
0.798s, with zero failures or skips. They cover unchanged v1 golden digests,
versioned typed sources/transfers, and rejection of cross-version or broken
chains. Migration files are unchanged at this checkpoint.

The contract distinguishes full protected lineage from
`GatewayCurrentAuthorityRef`, the kind/operation/profile/receipt projection SQL
can actually prove. Legacy upgrade SQL never stored protected generation,
identity or journal data, so those facts must come from protected observation.
Native successful grant, disable and startup outputs now explicitly populate
`EffectiveProfile` while retaining raw `Profile`.

The controller URL adapter now requires a valid, digest-matching effective
profile containing the allocation's port. It uses that profile's address while
preserving raw desired-allocation and observation-request identities. A focused
regression first failed because the existing adapter returned the original
address despite an authorized successor projection; it passed after the adapter
change. The complete selected controller run passed all five discovered
top-level tests and 15 refusal subcases in 18.819s, with zero failures or skips.
Its fixture now retains a real committed upgrade claim before granting access.

This checkpoint proves presentation and format contracts, not an actual
completed rebind. The successor projection is controlled test output. Matching
the callback's fresh SQL authority against the runtime's independent protected
observation is still required. Atomic SQL commit, complete transfer resolution,
runtime recovery and final fence release remain under implementation.

At frozen source `ee3950219111373c6c573bccb31e07c35ab23fef`, the complete related
package suites then passed:

```text
go test -mod=readonly -p=1 -json -count=1 -timeout=12m ./internal/appaccess ./internal/controller
```

All 206 top-level tests passed, with zero failures or skips. The package pass
events reported app-access 58.213s and controller 59.318s. This checks existing
grant, disable, startup and controller behavior alongside the new format and
projection tests; it does not cover the still-unimplemented cross-store writer
or runtime recovery.

The live handover test now extracts its existing pre-rebind two-application
source into `newLiveGatewayRebindSourceFixture`. That helper creates the real
original upgrade, LAN grant and loopback-only application; it creates no rebind
claim or successor resource. The existing handover wrapper retains its original
prepared-only claim, staging sequence and cleanup registration. Future
cross-store journeys must register their own phase-aware, receipt-bound
successor cleanup. This avoids reusing the old cleanup after its SQL/history
assumptions stop being true.

The extraction passed integration-tag compilation for generated-ingress
(0.841s) and hostd (0.842s):

```text
go test -mod=readonly -tags=integration -run '^$' ./internal/generatedingress ./cmd/hostd
```

That command intentionally runs no tests and is compilation evidence only.
The modified live helper has not run against Docker locally; the local daemon
remains unavailable.

### Published prerequisite jobs

At PR #135 head `e46185ff61b3fa519304e5fa0ce8ab9c1a788acb`, the
[gateway v2 job](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694246)
completed successfully. Its actual logs record:

- `TestLiveGatewayV2UpgradeCommitAndRestart`: passed in 38.34s.
- `TestLiveGatewayV2BindConflictRollsBack`: passed in 21.62s.
- `TestLiveGatewayRebindPassiveDockerSendsNoApplicationRequests`: passed in 32.51s.
- `TestLiveGatewayRebindPublicPassivePredecessor`: passed in 40.27s.
- The required complete gateway cleanup step passed.

The same exact PR #135 head subsequently passed the
[config-volume staging job](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694410).
Its actual log contains one pass for
`TestLiveGatewayRebindSuccessorConfigVolumeStage` in 58.16s, with no failure or
skip for that named test. The required complete config-volume cleanup passed.
This closes the earlier observed mount-order/readback failure for this named
journey on the corrected head; it does not prove the later config-copy/start
or private handover journeys.

The same head also passed the
[stopped-successor container job](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694390).
Its log records exactly one pass for
`TestLiveGatewayRebindSuccessorStoppedStageContainer` in 73.67s, with no named
test failure or skip. The required complete stopped-container cleanup passed.

At that same PR #135 head, the
[stage-config-copy job](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694377)
subsequently passed both actual Docker journeys:

- `TestLiveGatewayRebindSuccessorStageConfigCopy`: 100.08s.
- `TestLiveGatewayRebindSuccessorStageConfigCopyLostAcknowledgmentAdopts`: 87.79s.

Both named tests passed exactly once with no failure or skip, and required
stage-config-copy cleanup passed. This closes the earlier observed empty-volume
rejection for this corrected stage-copy source, including lost-acknowledgment
adoption.

At PR #136 head `bdbb697f767da4e71664da1434a75e6daec4b732`, the
[network-staging job](https://github.com/tyhuang9/rig/actions/runs/37374324911/job/111978936326)
passed `TestLiveGatewayRebindSuccessorNetworkStage` exactly once in 45.76s with
no failure or skip. Required network cleanup passed. These are prerequisite
results; the two final-config-copy jobs remained queued at the latest check.

PR #136's CodeRabbit status was successful, but its actual comment says draft
review was skipped, and there were no reviews or inline findings. That status
is not source-review evidence.

Further completed jobs were verified from their actual logs and exact job heads:

| PR and source | Named Docker test | Result | Job |
| --- | --- | --- | --- |
| #135 `e46185f` | `TestLiveGatewayRebindSuccessorNetworkStage` | One pass, 54.49s | [Network stage](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694496) |
| #135 `e46185f` | `TestLiveGatewayRebindSuccessorDataVolumeStage` | One pass, 66.17s | [Data volume](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694520) |
| #136 `bdbb697` | `TestLiveGatewayRebindSuccessorStoppedStageContainer` | One pass, 57.97s | [Stopped container](https://github.com/tyhuang9/rig/actions/runs/37374324911/job/111978936464) |

Each had no named test failure or skip and passed its required complete cleanup.
The PR #135
[guarded stage-start job](https://github.com/tyhuang9/rig/actions/runs/37373345314/job/111975694535)
ultimately failed all three journeys and cleanup. The direct case failed in
98.89s, lost-acknowledgment in 85.00s and compensation in 92.86s; all retained
containers were `Created`. Source inspection found the fixture assigned the
successor the same IPv4/port still occupied by the running predecessor.

The reviewed test-only correction uses two distinct private addresses on a
test-owned dummy adapter, requires explicit network opt-in, and adds adapter
residue checks while retaining all runtime proofs. It was published as normal
additive updates to the already authorized drafts:

| Draft | Exact corrected head |
| --- | --- |
| [#134](https://github.com/tyhuang9/rig/pull/134) | `b938a7af629c1494193050ed505121bb5934394c` |
| [#135](https://github.com/tyhuang9/rig/pull/135) | `f2b7815c9b15f47d328cd4f38f6e32b905307cd2` |
| [#136](https://github.com/tyhuang9/rig/pull/136) | `46c8351c174f5c4ed788d04a733b61a5c72b1243` |

Compilation, focused static checks, named opt-in refusal, workflow YAML/Bash
syntax, documentation and independent source review passed for that correction.
The exact remote heads, bases and open draft states were verified. New hosted
Docker results remain required; the earlier named passes do not prove this
changed physical fixture. The original daemon stderr was not retained, so
acceptance must come from the corrected Docker run.

The correction is integrated additively into this local branch. Its handover
fixture now reuses the same owned-address helper while preserving the original
handover opt-in; adapter cleanup still runs after Docker and application cleanup.
Integration-tag compilation passed after that reuse (generated-ingress 0.956s;
hostd compile reused its cached result). The first refactor compile caught a
still-needed child-process `runtime` import, which was restored before the
passing recheck. No physical handover result is claimed.

The containing workflow was still running, so the completed job's metadata and
logs were read directly. This proves those named inherited journeys only.
PR #136's two final-config-copy Docker jobs remained queued at the latest check;
private handover Docker acceptance is still pending publication and execution.
No new SQL, Linux race, rebind cutover or second-device result is claimed.

On the corrected PR #134 head `b938a7af629c1494193050ed505121bb5934394c`,
two prerequisite jobs have now passed with their exact source revision and
required cleanup steps verified:

| Named Docker test | Result | Job |
| --- | --- | --- |
| `TestLiveGatewayRebindSuccessorStoppedStageContainer` | One pass, 72.19s | [Stopped container](https://github.com/tyhuang9/rig/actions/runs/37381954383/job/112005898965) |
| `TestLiveGatewayRebindSuccessorNetworkStage` | One pass, 56.69s | [Network stage](https://github.com/tyhuang9/rig/actions/runs/37381954383/job/112005899139) |

The corrected stage-start job and the two PR #136 final-config-copy jobs were
still queued. These prerequisite results do not yet establish the changed
two-address stage-start behavior.

Additional exact-head prerequisite results were subsequently read from their
completed job logs. Each named test passed once with no failure/skip, and every
job's required cleanup step passed:

| Source | Named tests and seconds | Job |
| --- | --- | --- |
| #135 `f2b7815` | `TestLiveGatewayRebindSuccessorStageConfigCopy` 79.25; `TestLiveGatewayRebindSuccessorStageConfigCopyLostAcknowledgmentAdopts` 71.94 | [Stage config copy](https://github.com/tyhuang9/rig/actions/runs/37381953709/job/112005903751) |
| #135 `f2b7815` | `TestLiveGatewayV2UpgradeCommitAndRestart` 40.12; `TestLiveGatewayV2BindConflictRollsBack` 21.94; `TestLiveGatewayRebindPassiveDockerSendsNoApplicationRequests` 32.60; `TestLiveGatewayRebindPublicPassivePredecessor` 40.82 | [Upgrade and passive proofs](https://github.com/tyhuang9/rig/actions/runs/37381953709/job/112005903979) |
| #136 `46c8351` | `TestLiveGatewayV2UpgradeCommitAndRestart` 34.76; `TestLiveGatewayV2BindConflictRollsBack` 17.47; `TestLiveGatewayRebindPassiveDockerSendsNoApplicationRequests` 26.64; `TestLiveGatewayRebindPublicPassivePredecessor` 31.79 | [Upgrade and passive proofs](https://github.com/tyhuang9/rig/actions/runs/37381954436/job/112005925276) |
| #136 `46c8351` | `TestLiveGatewayRebindSuccessorNetworkStage` 43.49 | [Network stage](https://github.com/tyhuang9/rig/actions/runs/37381954436/job/112005925497) |

PR #136 at `46c8351` then passed both actual inactive-final-config Docker
journeys, each exactly once with no failure/skip and successful complete cleanup:

- [Direct copy](https://github.com/tyhuang9/rig/actions/runs/37381954436/job/112005925626):
  `TestLiveGatewayRebindSuccessorFinalConfigCopy`, 106.94s.
- [Lost acknowledgment](https://github.com/tyhuang9/rig/actions/runs/37381954436/job/112005925602):
  `TestLiveGatewayRebindSuccessorFinalConfigCopyLostAcknowledgmentAdopts`, 125.82s.

The corrected PR #135 stage-start run at `f2b7815` instead reported only a
stopped-container cleanup error in each of its three tests, then failed the
residue check. The exact sequence-eight reader used by cleanup rejected the
Caddy autosave retained after a real start and stop. The focused test-only
correction at `50c67b5` uses the existing production reader with the exact
retained sequence-nine intent; no production parser or runtime behavior changed.
All five autosave regressions passed in 9.056s, vet/integration-tag compilation
passed, docs checks/build passed in 3.35s, and independent review approved the
frozen change. Details and the failed job link are in the
[stage-start evidence](./m3-rebind-stage-start-docker-gate-evidence.md#2026-10-05-cleanup-after-a-proved-stage-start).

Normal atomic fast-forward updates published this fix to the already-authorized
drafts. The exact open draft states and targets were rechecked:

| Draft | Updated head |
| --- | --- |
| #134 | `50c67b58ef3dc3291c27f2c13f7cf8d525d0af68` |
| #135 | `627beda512725d48c02dfb56b7dd4e984ec8426c` |
| #136 | `b46162ea4a97335976663770b17d2a44612cc3f9` |

The downstream #136 tree compiled after integration (ingress 0.755s, hostd
cached), and the correction is integrated locally at `ef37d46`. The preceding
final-config passes establish that source behavior; hosted CI must still verify
stage cleanup and rerun the final-config journeys at the new head. Publication
receipts are retained in `$TEMP/m3-stage-autosave-cleanup-20261005/`.

The corrected PR #134 head `50c67b58ef3dc3291c27f2c13f7cf8d525d0af68` has
now passed its actual
[stage-start Docker gate](https://github.com/tyhuang9/rig/actions/runs/37385339564/job/112017111886):

| Named test | Exact result |
| --- | --- |
| `TestLiveGatewayRebindSuccessorStageStart` | One pass, 81.80s |
| `TestLiveGatewayRebindSuccessorStageStartLostAcknowledgmentAdopts` | One pass, 64.36s |
| `TestLiveGatewayRebindSuccessorStageStartProofFailureWithdraws` | One pass, 65.87s |

No named failure or skip occurred. All steps, including complete stage-start
resource and owned-adapter cleanup, succeeded. This supplies the missing
physical acceptance for the distinct-address fixture and autosave cleanup;
it does not claim that later handover/cross-store runtime work is accepted.
The exact source SHA was checked in job metadata, with the log retained at
`$TEMP/m3-pr134-50c67b5-stage-start.log`.

The latest PR #136 head `b46162ea4a97335976663770b17d2a44612cc3f9`
subsequently passed all nine jobs in its
[M3 gateway v2 Docker workflow](https://github.com/tyhuang9/rig/actions/runs/37385339443).
The changed final-copy and stage-start tests were checked individually in the
completed job logs; each ran once, with no failure or skip:

| Named test | Seconds | Job |
| --- | --- | --- |
| `TestLiveGatewayRebindSuccessorFinalConfigCopy` | 122.39 | [Direct final copy](https://github.com/tyhuang9/rig/actions/runs/37385339443/job/112017115825) |
| `TestLiveGatewayRebindSuccessorFinalConfigCopyLostAcknowledgmentAdopts` | 91.63 | [Lost acknowledgment](https://github.com/tyhuang9/rig/actions/runs/37385339443/job/112017115651) |
| `TestLiveGatewayRebindSuccessorStageStart` | 90.55 | [Stage start](https://github.com/tyhuang9/rig/actions/runs/37385339443/job/112017115741) |
| `TestLiveGatewayRebindSuccessorStageStartLostAcknowledgmentAdopts` | 74.44 | Same stage-start job |
| `TestLiveGatewayRebindSuccessorStageStartProofFailureWithdraws` | 75.92 | Same stage-start job |

Each job reports the exact source SHA above and successful complete resource
cleanup. Logs are retained as `$TEMP/m3-pr136-b46162e-<job-id>.log`.
The PR remains open and draft, targeting `feature/hosting-m3-rebind-final-config-intent`.
Other lifecycle/race/Windows checks were still running at this observation;
this records the completed M3 Docker workflow, not overall PR readiness.
No private handover or cross-store Docker result is inferred from these tests.

Subsequent exact-head job metadata at `b46162e` confirms successful
[Windows controller verification](https://github.com/tyhuang9/rig/actions/runs/37385339455/job/112017116036),
including its full Go, vet/build, Windows protection, frontend and generated
contract steps. The generated-runtime race groups for
[runtime packages](https://github.com/tyhuang9/rig/actions/runs/37385339467/job/112021142485)
and [gateway v2](https://github.com/tyhuang9/rig/actions/runs/37385339467/job/112021142568)
also completed successfully. The ingress-remainder race group and combined
PostgreSQL/Linux race job were still active. These are results for the published
prerequisite head, not acceptance of this unpublished cross-store branch.

## Integrated cross-store checkpoints

### Integrated SQL checkpoint

The independent security/code re-review accepted the bounded SQL checkpoint
`5c2a349ef868265e5eef8d46a8efbc8f935fff14`. It confirmed capability consumption
before row visibility, one immutable terminal decision and atomic
profile/head/transfer changes. The reviewer used an exact detached source
tree and executable regressions; no formal security-scan artifact is claimed.

That checkpoint is integrated with the protected current-state work at local
commit `fe2a6689a76c22bc244845a439863d0aec11b2ae`. The parent then ran exactly
11 named database/app-access regressions with `go test -mod=readonly -p=1
-json -count=1 -timeout=8m`: guarded migration/mirroring, one-use capability
consumption/cancellation, atomic lifecycle, extra-roster refusal, retained
terminal decision, chained digest readback, typed source union and the three
canonical format tests. All 11 passed, with zero failures/skips; database
0.833s and app-access 4.588s. Named events are captured in
`$TEMP/m3-cross-store-fe2a668-sql-integration.jsonl`. The command was:

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=8m ./internal/database ./internal/appaccess -run '^(TestLANGatewayRebindGuardedCommitMigrationRetainsV1AndIsMirrored|TestGatewayRebindGuardConsumesExactCapabilityBeforeRowVisibility|TestGatewayRebindGuardCancellationAndTransactionRollbackNeverReviveCapability|TestGatewayRebindTransitionAppliesCompleteLifecycleAtomically|TestGatewayRebindTransitionRejectsExtraRosterTransferAtomically|TestGatewayRebindTransitionRetainsOneTerminalDecision|TestGatewayRebindChainedTransferReadbackUsesDigestValue|TestGatewayRebindV2SourceClaimAcceptsUpgradeGenerationAndRejectsCrossKindMixes|TestGatewayRebindV1CanonicalDigestsRemainStable|TestGatewayRebindV2CanonicalTypesBindTypedSourceAndTransferChain|TestGatewayRebindV2CanonicalTypesRejectCrossVersionAndBrokenChains)$'
```

This is SQL integration evidence, not runtime completion.

The combined checkpoint also passed four exact generated-ingress regressions
in 37.211s, with no failures/skips: native proof matching, SQL authority/transfer
chain matching, proof population on native observation, and holding the gateway
lock through observation/resolution. The command was:

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^(TestGatewayV2LANEffectiveBindingProofMatchesSQLAuthorityAndChain|TestGatewayV2LANEffectiveBindingProofMatchesNativeUpgradeAuthority|TestGatewayV2LANEffectiveBindingProofIsPresentOnNativeObservation|TestGatewayV2LANObservationAndResolutionHoldGatewayLock)$'
```

Captured events: `$TEMP/m3-cross-store-fe2a668-proof-integration.jsonl`.

Startup source mapping found that `runServer` takes ordinary worker admission
immediately after opening SQLite. That closure checks the rebind fence before
`inspectGatewayStartup`, so recovery added only inside the latter would be
unreachable for active rebinds. The runtime implementation must add dedicated
startup recovery under the effects lease before ordinary admission/inspection;
ordinary workers retain their strict fence. Recovery must also precede
historical disable acknowledgment, LAN quarantine and normal ingress recovery.
The coordinator owns effects-lease acquisition; startup must call it before
taking ordinary startup admission, without nesting the same lease.

### Integrated proposal inspection

The bounded independent review accepted the runtime proposal/current inspection
checkpoint `5c8f982913a2f7b2258b2a2f253effe3009fbc6f`. It validates SQL/protected
source agreement under the gateway lock, builds the roster from raw grant
provenance plus validated effective transfers, repeats its observations, and
keeps the selected current source separate from any active operation. Its
cleanup projection exposes only identities from validated terminal receipts;
preterminal resources still require retention and phase-specific recovery.
That checkpoint and the focused cleanup correction are integrated locally at
`17e1aeecbc01e63cb108db1d63fa13771fbad252`.

Review of the next resolver checkpoint found two required corrections before
integration. Every transfer must retain the original raw source profile across
A-to-B-to-C changes. Also, the next protected generation cannot be computed as
the selected source generation plus one: rollback or a crash after SQL admission
can consume an attempt generation without changing the current source.
The unpublished v2 spec therefore binds `SuccessorProtectedGeneration`, selected
above retained SQL claims and protected history, with exact equality required
in transition proofs and the SQL guard. V1 canonical bytes and historical
migrations 026, 033 and 034 remain unchanged.

The corrected resolver/admission checkpoint and final SQL generation equality
guard passed bounded independent review at
`01621f68bc5cd4286e01de59ec5ce35635398b7a` and are integrated at
`0e08be73f09f078dd97716f83dd56f2917f90ec3`. The independent guard regression
and mirrored migration tests each passed (app-access 0.750s, database 0.978s),
with zero failure/skip. Historical disabled/released startup readers and the
post-rebind upgrade-head rule still require completion; this is not full-unit
approval.

The parent has a pending real-boundary proposal test and a dedicated CI job.
The test uses actual native SQL/protected/Docker state, compares repeated and
fresh-manager proposals, and checks unchanged authority, raw access, runtime
heads, resource ownership and routed-request counts. The CI guard requires the
named test to pass exactly once with no failure or skip, and checks complete
resource and owned-adapter cleanup. The repository's existing YAML parser,
Go syntax parser and `bash -n` accepted these pending files. After resolver
integration, `go test -mod=readonly -p=1 -tags=integration -run '^$'
./internal/generatedingress ./cmd/hostd` passed type checking (ingress 0.772s,
hostd 0.765s). A separate named discovery check with the common Docker opt-in
disabled found exactly the new proposal test and both existing handover tests,
all three skipped as required (package 0.834s). The log is
`$TEMP/m3-cross-store-live-opt-in-discovery.jsonl`. These skips are not physical
acceptance. The runtime proposal's generation adaptation and actual Docker run
remain pending; this is not commit/recovery acceptance. Documentation workflow
checks and the VitePress build also passed (3.66s).

### Verified controller comparison

The independently reviewed protected current-state checkpoint
`25fec6f573ac12c2174a7472aeaa4a6552ac4f0b` is integrated locally. Its native
observation carries the effective profile and full protected lineage under the
existing gateway lock. The controller must compare that proof with a fresh SQL
authorization from inside the same callback, including the complete transfer
chain. Neither empty SQL authority nor a raw-profile fallback is acceptable.

Before adding this comparison, the new controller regression was run against
the prior adapter:

```text
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/controller -run '^TestLANAccessStatusWithholdsURLWhenEffectiveProofDisagrees$'
```

All 11 cases first established a verified native baseline, then failed because
the response still exposed its URL after the protected proof was removed or
changed. The package failed in 3.634s as expected. The captured log is
`$TEMP/m3-cross-store-controller-effective-proof-red.jsonl`. This is evidence
for the controller gap, not a passing acceptance result. The adapter changes
and positive/refusal tests were subsequently integrated with the reviewed SQL
resolver and its full authorization DTO.

The corrected adapter compares the effective binding proof with fresh SQL
authorization inside the gateway-locked callback, before serving-head evidence
and URL assignment. Bounded independent code/security review accepted these
eight production lines and the corresponding tests with no blocking finding.
The focused check passed all seven named top-level tests and 32 refusal cases,
with zero failure or skip, in 22.955s:

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./internal/controller -run '^TestLANAccess(VerifiedStatus|StatusWithholdsURL)'
go vet -mod=readonly ./internal/controller
```

Vet, gofmt and diff checks passed. Green test events are retained at
`$TEMP/m3-cross-store-controller-effective-proof-green.jsonl`. Both newly added
negative suites first establish a verified positive baseline, then require URL
and timestamp withholding after native-proof or transfer-proof disagreement.

The adapter checkpoint is committed locally at `8f17a4c`. The full controller
package then passed all 115 top-level tests with zero failure/skip in 89.404s,
checking existing endpoints as well as the new proof requirement:

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=12m ./internal/controller
```

Full events: `$TEMP/m3-cross-store-8f17a4c-controller.jsonl`. PR #136's
description was also updated with its exact `b46162e` Docker acceptance above;
head, base and open-draft state were rechecked unchanged.

The controller's two-transfer fixture explicitly simulates validated DTOs;
it verifies projection and comparison only. Real SQL transfer, restart and
Docker acceptance remain separate required gates.

## Recovery and rollback boundary

### Pending startup and repository boundary checks

The parent now owns `cmd/hostd` startup integration. Dedicated rebind recovery
must precede ordinary effects admission, historical disable acknowledgment,
quarantine and worker recovery. A fresh or runtime-disabled installation must
first prove absence without constructing a Docker-backed Manager. The shared
presence probe must read validated SQL and protected namespaces without creating
directories, changing permissions, or treating orphan current-route files as
absence. Retained terminal rebind history requires reattestation even when
there is no active operation.

The new real-repository proposal/admission regression initially failed at the
proposal boundary on parent `8f17a4c` (package 1.099s):

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayRebindProposalAndAdmissionUseRealRepository$'
```

Captured events: `$TEMP/m3-cross-store-real-repository-red.jsonl`. The runtime
proposal still needs the reviewed explicit-generation adaptation. The test uses
real SQL and protected files with a simulated Docker runner; it will separately
check approval refusal, exact admission/replay, unchanged current authority,
the prepared fence and no protected/Docker effects. Its approval-error
expectation follows the existing V2 API's `ErrInvalidInput` classification.

Independent review held the later app-access candidate `ead9ece` after proving
that its relaxed historical-upgrade rule incorrectly accepted a still-current
native upgrade whose administrator was demoted. The positive baseline passed
first; the demoted case then failed its required-refusal assertion. The exact
overlay regression ran against a clean detached source tree and failed in
0.712s, one named failure:

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=3m -overlay C:/Users/huang/Documents/Projects/Rig/temp/cross-store-review-native-admin-overlay.json -run '^TestCrossStoreReviewCommittedNativeUpgradeDemotionRefusesCombinedStartup$' ./internal/appaccess
```

Log: `C:/Users/huang/Documents/Projects/Rig/temp/cross-store-review-native-admin-red.jsonl`.
That candidate is not integrated in the parent; the earlier `01621f6` guard
remains in place while the current-versus-historical authorization fix is made.
New presence regressions cover fresh real storage, prepared SQL, terminal
history without an active claim, orphan/reserved artifacts, unsafe paths,
permissions, cancellation and changes during confirmation. Their execution
awaits the corrected repository and runtime presence implementation.

The corrected app-access checkpoint
`d25531e222a887fadbbd14d2e37dd66a995c9170` passed bounded independent review
and is integrated locally at `dea357c448b70e0bfcfc0715cdbe7d5e7f7fee5e`.
Its native-source path retains administrator, current-head and multiplicity
checks. A historical committed upgrade is accepted only when validated rebind
history proves its exact predecessor ancestry. Seven independently executed
regressions, including the original demoted-administrator case, passed with
zero failure or skip in 6.233s. The implementation agent's full app-access
suite passed in 101.830s. Independent events are retained at
`C:/Users/huang/Documents/Projects/Rig/temp/cross-store-review-d255-green.jsonl`.
This clears the earlier held SQL checkpoint only, not the complete runtime unit.

Independent execution of the pending startup presence tests against frozen
runtime `8b4212dd85c694825d0e7465e1d84de704e1f0d9` exposed two production
defects: valid native authority was mistaken for rebind history, and replacement
of an empty protected directory was missed on Windows. Windows `os.FileInfo`
can resolve identity lazily through its path; identity must be captured before
the second SQL read. The first run also exposed an overly broad test comparison
of opaque file metadata. The tests now use the existing domain snapshot
comparison, retaining file identity, size, mode, modification time and content
digest checks, with Docker-command counts asserted separately. The initial
events are retained at `temp/cross-store-review-startup-presence-red.jsonl`.

At the subsequent frozen runtime `281cd3e`, native and prepared presence tests
passed independently in 0.69s and 0.70s. The actual repository test then reached
admission and correctly refused its unfinished fixture deployment. The fixture
now explicitly verifies that refusal leaves no claim or fence, finishes only
its exact serving deployment and related job, and verifies the real terminal
census before retrying admission. Complete presence and repository results
still await the corrected runtime checkpoint and retest.

### Integrated startup presence and adapter results

Runtime checkpoint `17fbafb4cc990349a4390ca6eda41970fe097d6d` passed bounded
independent review and was integrated at `bbfb647b8081405a014a74ad2c2b9f33a5f8a01b`.
This includes the post-claim clock correction and eager directory identities.
The parent then ran all 11 named startup-presence tests: ten passed, the POSIX
permission-preservation test was explicitly skipped on Windows, and none
failed (21.857s). Both symlink cases ran and passed. The passing test checkpoint
is `ff08266`; events are `$TEMP/m3-cross-store-bbfb-presence.jsonl`.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./internal/generatedingress -run '^TestGatewayRebindStartupPresence'
```

The startup projection adapter at `639dadd2338a11adf51810b43089ad1914fee96d`
passed bounded independent code/security review. It carries current and retained
profile/source/transfer/receipt evidence separately, preserves immutable raw
requests, and copies transfer slices and nested predecessor-digest pointers.
Partial or mixed evidence remains visible for runtime rejection. A raw profile
alone never manufactures effective authority. Existing `RequiresRecovery`
decisions remain unchanged until runtime consumers validate the projection.

Four new mapping tests and 14 existing startup/recovery tests passed with zero
failures or skips (0.750s), including real migrated legacy-pair composition,
historical-disable acknowledgment and batch selection. Vet and formatting
checks passed. Events: `$TEMP/m3-startup-binding-preservation.jsonl`.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./cmd/hostd -run '^Test(LANStartupMapping|MigratedLegacyPairRecoveryComposition|LANGrantStartup|LANDisableStartup|LANAccessStartup|LANRecoveryBatch|AttestHistoricalLANDisableSuccessor|HistoricalLANDisableSuccessorSelection)'
go vet -mod=readonly ./cmd/hostd
```

The real repository test then exposed a production integration error: the fence
reader used the legacy snapshot and rejected valid v2 prepared state as corrupt.
The reviewed correction `1312fbe50ec5fa2bcd68803e08aac9cfac072f72` uses the
version-aware recovery snapshot and preserves refusal of corrupt history.
Three independent regressions passed in 3.832s; the implementation agent's full
app-access suite passed in 93.190s. The parent integrated it at
`dd2f6bb22b17706b532bde30fb3e57962fef64c3` and the real repository
proposal/refusal/admission/replay test passed with no failure or skip.
Events: `$TEMP/m3-cross-store-real-repository-green.jsonl`.
The final rerun explicitly checked absence of both v1 and v2 protected intents
and passed in 1.313s (named test 1.06s). Repository-wide
`go build -mod=readonly -buildvcs=false ./...` and
`go vet -mod=readonly ./...` passed. Documentation workflow checks and the
VitePress build passed (3.72s). These are checkpoint checks, not the complete
unit's final suite or a Docker result.

The strengthened real-repository prepared-admission regression also passed
bounded independent review. Both normal preparation and an injected return
failure immediately after the real SQL claim commit recover through a fresh
Manager, with the production effects/Manager/gateway locks held. Recovery
preserves predecessor file identity and content, creates exactly one checkpoint,
typed intent and first progress record, replays those exact records, and retains
the SQL fence and original current authority. No Docker command is issued.
The network inventory is simulated; the production network selector and
observation canonicalizer are exercised. The return fault is not process-kill
or physical Docker acceptance evidence.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayRebind(ProposalAndAdmission|PreparedAdmissionAndRecovery)UseRealRepository$'
```

Both named tests and both boundary subtests passed, with zero failures or skips
(4.167s). Events: `$TEMP/m3-cross-store-real-prepare-green.jsonl`.
Formatting and `git diff --check` passed.

The prepared SQL boundary still has no Docker effects or protected commit
receipt. Full physical coordination, terminal recovery, transfer-aware normal
operations and startup dispatch remain required for delivery.

### Startup authority validation checkpoint

SQL checkpoint `b91f55d6ef43c78cb3f83c1314bf5b00aaea143b` was independently
reviewed and integrated at `ab38e9e`. In-flight grants and no-source disables
carry optional SQL authority only when their raw profile is still exactly
current. Four independent tests passed with no failure or skip (7.580s), and
the implementation agent's full app-access suite passed (100.641s). Evidence:
`temp/cross-store-review-b91-startup-authority.jsonl`.

The startup grant/disable claim validators now check canonical effective
profiles, typed source and receipt, immutable raw app/allocation/grant/profile
identity, ordered digest-linked transfer history and its terminal tip. Current
and retained evidence are mutually exclusive and must agree across the exact
grant/disable pair. Native grants on a rebound profile and no-source in-flight
disables have explicit no-transfer rules. The regression includes semantic
mutations with recalculated transfer hashes and a projected two-transfer chain;
it does not claim a physical repeated rebind.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./internal/generatedingress -run '^TestGatewayV2LAN(Startup|DisableStartup|Access)'
go vet -mod=readonly ./internal/generatedingress
```

All 21 top-level tests passed, zero failures or skips (32.622s), including the
existing native startup/quarantine and disable cases. Vet and diff checks
passed. Events: `$TEMP/m3-startup-binding-consumers.jsonl`. Bounded independent
review found no issue in this structural validation checkpoint. Selection of
the protected current generation and physical attestation remain required;
these validators alone do not authorize serving.

The reviewed runtime reader/attempt checkpoint
`6f2018d349df9b39b7e0491df1c0904d7b663fe0` is integrated at `f2b1761`.
Independent testing first reproduced the missing typed-intent comparison at
`76160ae` (17.661s, one expected regression failure). The corrected checkpoint
passed four independent reader/barrier tests without failure or skip (37.528s).
The attempt adapter's separate three-test review passed (19.236s). Logs:
`temp/cross-store-review-761-typed-history-red.jsonl`,
`temp/cross-store-review-6f-reader-barrier-green.jsonl`, and
`temp/cross-store-review-761-attempt.jsonl`.

Both production controller constructors now inject the app-access current-state
repository. The shared optional-selection helper requires that provider even
for an absence decision. Fresh or configured/preupgrade SQL must contain no
rebind history, active claim, transfer, phase or commit evidence, and two
nonmutating protected scans must agree around the confirming SQL read. Existing
authority uses the strict SQL-led selector. Empty SQL plus lost files cannot
be inferred from a missing provider. The legacy unit fixture supplies explicit
empty-history data; authority-specific fixtures must override it.

Five helper test groups passed with zero failures or skips (18.606s), covering
real fresh/native repositories, partial SQL, orphan files, changed SQL or paths,
cancellation and terminal-history dispatch. The terminal case uses real
protected fixtures with simulated SQL and does not establish physical serving.
Its first run exposed two test-setup mistakes: an uninitialized fixture mutex
and sample ports outside the supported range. The stalled process was stopped,
both fixtures corrected, and the complete named group rerun successfully.
Events: `$TEMP/m3-current-optional-selection-final.jsonl`; previous attempts are
retained in `$TEMP/m3-current-optional-selection.jsonl` and
`$TEMP/m3-current-optional-selection-green.jsonl` (the latter failed despite its
filename).

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestOptionalGatewayCurrentSelection'
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./cmd/hostd -run '^Test(GatewayStartupRebindFence|RuntimeCompositionRebindFence|GeneratedComposition|PrepareRuntimeWorker|DeploymentEffectsAdmission|LANStartupMapping|MigratedLegacyPairRecoveryComposition|LANGrantStartup|LANDisableStartup|LANAccessStartup|LANRecoveryBatch|AttestHistoricalLANDisableSuccessor|HistoricalLANDisableSuccessorSelection)'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

All 37 controller composition/startup/effects tests passed (22.899s, zero failure
or skip). Events: `$TEMP/m3-current-provider-composition.jsonl`. Vet, build and
diff checks passed. Independent code review accepted the helper and constructor
changes. Rebound startup consumers and recovery-before-admission dispatch are
still pending; constructor injection alone does not satisfy those requirements.

Native gateway startup now performs that strict optional selection before and
after its existing physical proof. It rejects active rebind recovery, missing
providers, changed SQL authority and changes to the protected selection. The
exact native generation used for physical proof is compared with the
SQL-selected observation, including the original route source. The added
regression uses projected SQL and the existing fake physical driver; an injected
valid protected-file replacement must refuse before the driver is invoked.
The normal protected writer correctly rejected the initial replacement fixture,
so the final test explicitly injects the external write through the test's
protected-file writer. Production transition checks were not relaxed.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayV2(Startup|LANStartup|LANDisableStartup|LANAccess|LANRecovery)'
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayV2Startup'
```

The expanded existing startup/LAN/recovery set passed all 68 top-level tests
(57.235s). After the final selected-generation comparison was added, all seven
native startup tests passed again (10.898s). Both runs had zero failures or
skips. The same 37 controller preservation tests also passed again (22.779s).
Vet and diff checks passed. Logs are
`$TEMP/m3-native-startup-current-preservation.jsonl`,
`$TEMP/m3-native-startup-current-confirmed.jsonl`, and
`$TEMP/m3-native-current-hostd-preservation.jsonl`. Bounded independent review
accepted the final diff. This native inspection still refuses SQL-selected
rebind authority until its separate typed-terminal/physical-attestation branch
is implemented; it does not represent completed rebound startup.

PR #136 received the separately reviewed CI partition correction at `7deefa4`.
Its M3 Docker workflow passed again at that exact revision (run `37391508257`).
The previous repository-wide race job at `b46162e` was cancelled by normal
workflow concurrency after the update; its PostgreSQL-specific steps passed,
but its repository-wide result is unverified. New race batches at `7deefa4`
all passed, including the aggregate check (run `37391508420`). Windows passed
at the same head (run `37391508875`). The repository-wide race step in run
`37391508290` subsequently hit its 40-minute ingress package timeout, without a
preceding race report or failed assertion. The original published correction is
integrated locally at `3cf6bb8`. The reviewed follow-up `9694b4e` moves that
workflow onto the same shared ingress partitions and retains all other 49
packages, PostgreSQL checks, and the original required aggregate check. It is
integrated locally at `e1c4d49`. New exact-head runs are
[repository race](https://github.com/tyhuang9/rig/actions/runs/37399509007),
[lifecycle race](https://github.com/tyhuang9/rig/actions/runs/37399508966), and
[M3 Docker](https://github.com/tyhuang9/rig/actions/runs/37399509055). Their full
results remain pending. The failed run and correction are recorded in the
[final-copy evidence](./m3-rebind-final-config-copy-evidence.md).

Keep the new runtime path unavailable until its complete invariants and gates
are satisfied. Reverting source must not delete protected receipts, committed
profiles, transfer history or uncertain resources. Before a database commit,
only exact proved abort recovery can roll back. After a historical database
commit, preserve successor state and recover forward. External databases remain
application-owned through scoped runtime secrets; this work adds no managed
database or Neon provisioning.

### Native LAN startup authority checkpoint, 2026-10-05

Integrated the independently reviewed current-route persistence checkpoint
`4cfa69236546bee7d6d604ece38de27f064b4dd1` at `d2cfa74`. Its two independently
executed tests passed in 39.354s, with zero failure or skip. That checkpoint
covers quiescent persistence, immutable raw bindings/transfer manifest, and
refusal while another rebind is active; the ordinary mutation state machines
remain work in progress.

Both read-only native LAN startup inspections now bind SQL current authority
to the exact protected native store, journal and route state before physical
observation and confirm the same authority afterward. Effective and retained
projections must match the actual protected profile and operation. Retained
history remains separate from serving authorization. Missing providers,
missing required projections, forged source/interface, active rebind and SQL
drift refuse startup. Explicit SQL absence preserves the legacy no-projection
path; it cannot authorize a supplied current or retained projection.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./internal/generatedingress -run '^TestGatewayV2(Startup|LANStartup|LANDisableStartup|LANAccess|LANRecovery)'
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayV2(StartupSQLSelectedNative|LANStartup(BindsNative|RetainedNative|SelectedNative))'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
```

The preservation run passed all 72 top-level tests in 60.878s; the final
five-test run, including the added SQL-selected native Pending positive,
passed in 4.876s. Both runs had zero failure or skip. Vet and diff checks
passed. Logs are `$TEMP/m3-native-lan-authority-preservation.jsonl` and
`$TEMP/m3-native-lan-authority-confirmed.jsonl`. The first focused run failed
because its assertion included the fixture's initial grant apply count;
the fixture counter was reset before inspection, with no production relaxation.
Its failed log is `$TEMP/m3-native-lan-authority-first.jsonl`.

The tests use real protected storage and locks, projected SQL reader responses
and bounded fake physical drivers. They cover SQL-selected native Pending and
LANRecovery startup, but are not Docker acceptance. Independent review accepted
this checkpoint at `6b9bbce` with no blocker. Rebound physical consumers, process
recovery before ordinary admission and the complete cross-store coordinator
acceptance remain outstanding.

Native startup quarantine now revalidates authority before its protected
mutation and at the physical apply boundary. At that boundary a fresh protected
pending marker must derive the exact requested withdrawal; SQL authority,
native journal and original route source must remain unchanged. The existing
driver only receives that validated withdrawal. A final re-read is required
before success, including paths that need no apply. Uncertainty preserves the
pending record; no terminal receipt or SQL history is rewritten.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayV2(LANStartupQuarantine|LANDisableStartup)'
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayV2LANStartup(QuarantineRechecksSQLAtEffectBoundary|RetainedAuthorityCannotServeLiveGrant)$'
go test -mod=readonly -p=1 -json -count=1 -timeout=4m ./cmd/hostd -run '^Test(GatewayStartupRebindFence|RuntimeCompositionRebindFence|GeneratedComposition|PrepareRuntimeWorker|DeploymentEffectsAdmission|LANStartupMapping|MigratedLegacyPairRecoveryComposition|LANGrantStartup|LANDisableStartup|LANAccessStartup|LANRecoveryBatch|AttestHistoricalLANDisableSuccessor|HistoricalLANDisableSuccessorSelection)'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The seven existing quarantine tests passed in 4.630s. Two new top-level tests
passed in 7.411s, including ten grant/disable success and fault cases: forged
projection, SQL drift before the pending write, after the pending write, and
after apply. They assert exact retained raw routes/journal, pending marker
retention, zero apply on unproved authority, and refusal after late SQL drift.
The controller set passed all 37 tests in 22.426s. All runs had zero failure
or skip; vet and full build passed. Logs are
`$TEMP/m3-native-lan-quarantine-preservation.jsonl`,
`$TEMP/m3-native-lan-quarantine-boundaries.jsonl`, and
`$TEMP/m3-native-lan-quarantine-hostd.jsonl`. A Git diff check attempted from
the elevated test identity could not recognize the worktree; its separate
non-elevated retry passed. No Git mutation was attempted by that failed check.

These quarantine tests retain the same projected-SQL/fake-Docker limitation.
They do not prove the pending typed-rebind or repeated-rebind runtime paths.

Independent review of `3fc4e6b` required two corrections before acceptance:
the final check must compare the exact pending state just proved, and the
disable mutator must propagate gateway lock-release failure. Both findings
were reproduced first: two top-level regressions failed in 2.733s at the
reviewed revision (`$TEMP/m3-native-quarantine-review-red.jsonl`). The protected
read seam deterministically returned the core's correct pending bytes while
installing a different valid native state immediately afterward, without
changing SQL, source or journal. Both grant and disable incorrectly succeeded.
The second regression released the actual lock, then injected its release
error; the disable mutation incorrectly returned success.

The correction retains the exact apply-boundary marker for a new grant
withdrawal and compares it again after the core proof. Existing pending and
disable paths compare their already-known exact state. The disable method
now uses the same named-result release guard as the grant method.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayV2(LANStartupQuarantine|LANDisableStartup|LANStartupRetainedAuthority)'
```

All 11 top-level tests passed in 14.002s with zero failure or skip
(`$TEMP/m3-native-quarantine-review-green.jsonl`). Vet, full build and diff
checks passed again. These changes preserve all prior failed-test evidence.

Independent review accepted the combined `3fc4e6b` and
`7d3935ef7a1fca788ba5c54b42ee56d0d14232d6` checkpoint. Its separate frozen run
passed all three boundary/regression tests in 9.777s, with zero failure or
skip (`Rig/temp/cross-store-review-7d-quarantine-green.jsonl`). This acceptance
covers native startup/quarantine only; production rebound physical attestation,
the coordinator and full runtime delivery acceptance remain pending.

### Current physical contract and prepared-process fixture checkpoint

Integrated the reviewed current physical-transition contract
`4a191437c1dca2ecf71f78fd8a7e4896f2b07205` at `7e76ebe`, then the selected-current
compensation contract `693ccd84f48def503f90ec5b10b091875390eb3b` at `f1729e4`.
The former requires exact before/effective projections at the selected protected
revision; a stopped outcome requires listener absence. It binds transferred
withdrawals to their immutable raw grant and effective profile separately.
Independent review first reproduced the unrelated-route acceptance defect at
the earlier `eaaa3dd` checkpoint, then passed two top-level tests and three
subtests in 34.120s after correction. Logs are
`Rig/temp/cross-store-review-eaaa-physical-boundary-red.jsonl` and
`Rig/temp/cross-store-review-4a-physical-boundary-green.jsonl`.

The compensation contract restores the exact captured grant or stops only the
selected current resources after an uncertain save. It does not change the
distinct predecessor-retirement operation. Its two independent tests passed in
33.570s with no failure or skip
(`Rig/temp/cross-store-review-693-compensation.jsonl`). These are contract tests;
the production physical adapter still refuses unavailable operations. They do
not prove ordinary route/LAN mutation or complete rebind recovery.

Added two disposable-Linux Docker gate cases to the local runtime branch:

- Read-only proposal inspection against actual SQLite, protected history, and
  two running application containers. Repeat/fresh-manager inspection must
  retain SQL, raw grants, runtime heads, Docker identity and request counts.
- An actual child-process exit immediately after the SQL claim commits, before
  protected admission writes or deferred lock cleanup. Two separate recovery
  processes must recover and replay the same typed prepared intent without
  rewriting predecessor history or releasing the fence. The test verifies
  unchanged runtime heads, raw grants, request counts, Docker identity, both
  loopback routes, original LAN response, LAN rejection of the loopback-only
  application, and absence of the successor listener.

The shared live fixture now opens the real control database before its first
route switch and supplies that repository to every source manager. It builds
the counted image used by the new assertions. Review caught the initial
uncounted-image mismatch before publication; that fixture would have failed
its first request-counter read. No passing Docker result is claimed for it.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestLiveGatewayRebind(CrossStore|FinalHandover)'
go vet -mod=readonly ./internal/generatedingress
go build -mod=readonly -buildvcs=false ./...
pnpm --dir docs install --frozen-lockfile --offline
pnpm --dir docs check:workflow
pnpm --dir docs build
```

The corrected live discovery run compiled in 0.778s. Both new journeys, their
child helper, and both handover journeys skipped because live opt-ins were
unset; the existing inert handover child returned normally. This is compilation
and discovery evidence only (`$TEMP/m3-cross-store-live-corrected-discovery.jsonl`).
Go vet and the repository build passed. YAML parsing confirmed both exact test
names, opt-ins, one-pass/no-skip guards and always-run owned-resource cleanup;
all three extracted Bash blocks passed `bash -n`. Diff and formatting checks
passed. The offline frozen install succeeded; docs workflow/build passed
(3.59s) outside the sandbox after its identity was denied access to an installed
Vite manifest. The initial sandbox build failure is an environment limitation.

Independent review accepted the corrected local fixture/CI diff, including the
real DB cleanup lifetime and child-process boundary. Actual Docker execution,
hostd startup dispatch, terminal SQL recovery, repeated rebind and final fence
release remain unverified. This branch remains unpublished and is not ready
for runtime delivery acceptance.

### SQL-selected rebound startup classification checkpoint

The local prepared-process fixture was frozen at `e0c9bac`. The reviewed route
restore extension `cdbf174283792d5f31e1608a0b3a7b2b964b320e` was integrated at
`d52523a`; its worker-reported focused test passed in 17.509s and independent
review accepted the two-file contract diff. That extension returns the exact
pre-switch route while retaining the pending marker, including a new app whose
previous route was absent. The real physical adapter remains unavailable.

Startup now has a distinct read-only path for SQL-selected rebind authority.
It validates the entire historical native upgrade claim census, consumes the
shared current-generation physical attestation, and rechecks unchanged SQL,
protected history and current state before returning. It does not manufacture
an upgrade journal for a rebound generation. The inspection carries the current
gateway source separately from the raw grant or disable recovery identity.

An ordinary pending route continues through normal composition, whose existing
`ingress.Recover` runs before deployment workers; its correctness depends on the
reviewed ordinary recovery path restoring the committed route. LAN pending
operations and batches select recovery-only startup. Their downstream rebound
LAN consumers and quarantine are still outstanding; this checkpoint alone does
not admit an operational rebound controller.

The hosting startup census now reads complete rebind authority inside the same
SQLite transaction as upgrade, grant and disable claims. Controller mapping
requires an exact current profile/source and retained database-commit event
under a released committed claim. It rereads the complete census before
accepting a rebound inspection and retains raw per-app recovery identities.
Quarantine/retirement comparisons also bind the current gateway source.

```powershell
go test -mod=readonly -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayRebindCurrentStartupBindsSQLAndPhysicalAuthority$'
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayRebindCurrentStartupDistinguishesRouteAndLANRecovery$'
go test -mod=readonly -p=1 -json -count=1 -timeout=3m ./internal/appaccess ./cmd/hostd -run '^Test(HostingGatewayStartupRebindCensus|RebindStartupMapping)'
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./internal/generatedingress -run '^TestGatewayV2(Startup|LANStartup|LANDisableStartup|LANAccess|LANRecovery)'
go test -mod=readonly -p=1 -json -count=1 -timeout=5m ./cmd/hostd ./internal/appaccess -run '^Test(GatewayStartup|RuntimeCompositionRebindFence|GeneratedComposition|PrepareRuntimeWorker|DeploymentEffectsAdmission|LANStartupMapping|LANGrantStartup|LANDisableStartup|LANAccessStartup|LANRecoveryBatch|AttestHistoricalLANDisableSuccessor|HistoricalLANDisableSuccessorSelection|HostingGatewayStartup|GatewayUpgradeStartupSnapshot|AppAccessGrantStartupSnapshot)'
go vet -mod=readonly ./internal/generatedingress ./internal/appaccess ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The final authority test passed in 22.037s, including seven refusal boundaries:
missing/forged historical claim, unavailable SQL, active rebind, invalid physical
proof, SQL drift during proof, and a valid protected current revision replaced
during proof. Route, disable and batch cases passed in 57.190s and retain their
pending evidence. These tests use genuine legacy protected receipts, real
protected storage/locks, projected SQL and a fake physical adapter; they do not
establish typed-v2 lineage or Docker behavior.

The real two-handle SQLite concurrency test passed (package 0.703s): a terminal
release commits between census reads, while the ongoing startup read retains
its original database-committed snapshot; the next read sees the release without
changing transfers. Two controller mapping tests and twelve cases passed
(package 0.714s). Native preservation passed all 77 top-level tests in 76.195s;
controller preservation passed 36 in 21.795s and app-access preservation passed
12 in 8.815s. All final runs had zero failure or skip. Vet and full build passed.
Logs are `$TEMP/m3-rebound-startup-stable-final.jsonl`,
`$TEMP/m3-rebound-startup-recovery.jsonl`,
`$TEMP/m3-rebound-startup-mapping-corrected.jsonl`,
`$TEMP/m3-rebound-startup-native-preservation.jsonl`, and
`$TEMP/m3-rebound-startup-preservation.jsonl`.

Initial test-only setup failures are retained: the sandbox identity could not
construct the protected legacy fixture (`m3-rebound-startup-first.jsonl`); a
raw helper Manager had an uninitialized mutex and timed out at four minutes
(`m3-rebound-startup-elevated-first.jsonl`); its replacement initially omitted
required private Docker options (`m3-rebound-startup-corrected.jsonl`). The
fixture now uses `New` with real private directories. The first concurrency test
used one single-connection pool for a nested writer and timed out at three
minutes (`m3-rebound-startup-mapping-first.jsonl`); the corrected test opens a
second real database handle and bounds the operation context. Production
timeouts and safeguards were not relaxed.

Independent bounded source review found no blocker in the startup/census
checkpoint and requested the valid protected-replacement negative, which now
passes. Full physical rebind adapters, rebound LAN/batch consumers, early
process recovery dispatch, repeated rebind and final release acceptance remain
outstanding. This runtime branch remains unpublished.

The frozen startup checkpoint is
`38c9a360aac233165d5fa5d3de0d7875359aa13a`. Independent review from detached,
clean source passed all five named top-level tests and 22 subtests, with zero
failure or skip: app-access 0.787s, ingress 92.751s, and controller 0.753s.
The anchored run included the real SQL census test, both rebound startup tests
and both controller mapping tests. Its log is
`Rig/temp/cross-store-review-38c-startup.jsonl`. The source remained clean.
The checkpoint received bounded acceptance for census and read-only startup
classification; the runtime limitations above still apply. Documentation checks
and build also passed before freezing (3.92s).

### Startup handoff to ordinary current-route recovery

The reviewed route state machine `afd5718b69fbc6c1a887ce7c46e1716c83a05d90`
was integrated at `febcb20`. Independent verification passed three top-level
tests and three drift cases (80.600s), plus the existing native grant restart
and two legacy switch/compensation tests (1.603s), with zero failure or skip.
Logs: `Rig/temp/cross-store-review-afd-route.jsonl` and
`Rig/temp/cross-store-review-afd-native-preservation.jsonl`.

The combined startup/recovery test then passed one top-level test and both
physical outcomes (before and effective), zero failure or skip, in 46.144s:

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayRebindCurrentStartupRouteRecoveryBeforeAdmission$'
```

It exercises public startup inspection followed by public `Manager.Recover`
and another inspection. Inspection preserves the pending marker; recovery
restores the exact committed application routes and raw LAN bindings, advances
the operational revision, and preserves selected authority and immutable
gateway history. The test uses protected storage and a fake physical adapter;
it does not establish hostd process ordering or Docker acceptance. Log:
`Rig/temp/m3-rebound-startup-route-admission.jsonl`.

### Selected-current LAN startup census

Independent code review accepted the combined route/startup test `db49792`.
The independently reviewed binding matchers `4039d634` were integrated at
`1aa1f85`; their exact test passed in 17.568s, one top-level test, no failure or
skip (`Rig/temp/cross-store-review-4039-lan-matchers.jsonl`).

Both public LAN startup readers now select current rebind authority before
using the native path. They compare immutable raw grants and effective SQL
projections to actual current operational endpoints, preserve pending markers,
attest through the shared physical contract and reconfirm exact SQL/protected
authority after observation. Candidate ordering and collision rules are shared
with native startup. Retained authority cannot authorize an unmarked live
binding; an exact terminal-disable pending marker always selects recovery.

The first focused run passed two top-level tests and twelve cases in 34.962s
(`Rig/temp/m3-current-lan-startup.jsonl`). Expanded verification included native
pending grant/withdrawal, grant-only startup, SQL drift and protected revision
replacement after physical proof:

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(GatewayCurrentLANStartup|GatewayV2(LANStartup|LANDisableStartup|LANAccess|LANRecovery))'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayV2LANStartupBindsNativeProjectionBeforeAndAfterPhysicalProof$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The expanded run passed 66 top-level tests and 91 subtests, with no skips,
but failed one existing test (99.909s). That fixture depended on exactly six
SQL reads and injected drift on read four; the new selection adds reads and
made the injection occur before physical proof. The corrected fixture now
injects at the observed `prove_committed` event and asserts reads on both sides
of that boundary. Its focused rerun passed the one test and all twenty subtests
in 3.340s, with no failures or skips. No production validation was weakened.
Logs: `Rig/temp/m3-current-lan-startup-preservation.jsonl` and
`Rig/temp/m3-native-lan-startup-semantic-drift.jsonl`. Vet and full build passed.

These are projected-SQL/fake-physical tests with real protected storage, not
Docker acceptance. Rebound quarantine, ordered batch consumers and complete
historical retained-receipt validation remain outstanding. In particular,
cleared historical disables are not treated as serving authority, but their
full protected typed-history proof must be integrated before delivery. Disable
acknowledgment follows protected pending clearance and fresh withdrawal proof;
an acknowledged disable with a remaining pending marker is inconsistent.

PR #136's final hosted repository/PostgreSQL race workflow
[`37399509007`](https://github.com/tyhuang9/rig/actions/runs/37399509007)
completed successfully at its published head `9694b4e`. This evidence applies
to the published final-config-copy draft, not this unpublished runtime branch.

The LAN census checkpoint was frozen at `3186e9e`; documentation workflow and
build passed (3.51s). The following batch-presence integration adds only a
read-only current-generation path to `HasGatewayV2LANRecoveryBatch`, which the
controller calls before LAN inspection. It reconfirms exact SQL and protected
selection, preserving markers and refusing unavailable authority without any
physical observation or mutation. Existing native selection remains covered.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^Test(GatewayCurrentLANRecoveryBatchPresenceBeforePhysicalWork|HasGatewayV2LANRecoveryBatchSelectsIntentBeforeReload)$'
go vet -mod=readonly ./internal/generatedingress
```

Both named tests passed, with no failure or skip, in 20.240s. Vet and diff checks
passed. Log: `Rig/temp/m3-current-lan-batch-presence.jsonl`. This proves only
presence selection before effects; batch quarantine, physical proof, ordered
head finalization and retirement still require the pending runtime integration.

Independent bounded review accepted `3186e9e` and `e621d37` with those delivery
limitations. At frozen `e621d37`, all four selected tests and 34 subcases passed
in 56.883s with zero failure or skip. Log:
`Rig/temp/cross-store-review-e621-lan-startup.jsonl`.

### Pure ordered batch transitions for the selected current gateway

The next local change derives a batch from the complete validated startup
census, then provides explicit clear, advance and retirement state transitions.
It preserves immutable items and original pending evidence through every head;
each changed transition advances the operational revision exactly once. An
already-absent head returns an independent copy with identical bytes/revision.
It never writes files, resolves SQL callbacks or performs Docker operations.
The guarded runtime consumer must still prove physical withdrawal, bind the
requested head to the freshly selected state, resolve terminal SQL and clear
acknowledgment, then persist/reconfirm each exact transition.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayCurrentLANRecoveryBatchTransitionsPreserveQueue$'
go vet -mod=readonly ./internal/generatedingress
go build -mod=readonly -buildvcs=false ./...
```

The final focused test passed in 17.910s with no failure or skip. It exercises
an ordered two-application disable batch (transferred and native grants),
preserved raw identities and loopback routes, pending evidence, deep-copy
isolation, replay, invalid/completed head bounds, refusal to advance before
clearance, separate retirement, revision exhaustion, and a prepared grant with
no published binding. Vet, full build and diff checks passed. Log:
`Rig/temp/m3-current-lan-batch-state-complete.jsonl`.

Initial fixture failures are retained: the inherited fixture had only one
LAN grant (18.111s, `m3-current-lan-batch-state.jsonl`); after adding a native
grant, the single-disable helper reused one operation ID and was correctly
rejected (17.837s, `m3-current-lan-batch-state-corrected.jsonl`). Distinct
application/operation identities fixed the fixture. The first complete
two-disable run passed in 17.958s (`m3-current-lan-batch-state-final.jsonl`),
before adding prepared-grant coverage. These pure transformations are a
prerequisite for the guarded batch consumer, not runtime or Docker acceptance.

Independent review accepted frozen `4147037`: the named batch transition test
passed in 18.215s with no failure or skip
(`Rig/temp/cross-store-review-414703-batch-state.jsonl`).

### Ordinary LAN integration and transferred committed recovery

Integrated independently reviewed ordinary current-generation LAN state
machines at `2b514bc` through local merge `c567f9d`. The independent frozen
boundary run passed three top-level tests and three subcases in 119.867s,
with no failures or skips (`Rig/temp/cross-store-review-2b514-lan-boundaries.jsonl`).
It covers exact request/persistence boundaries, cancellation and uncertain
writes, proof drift before SQL resolution, and disable resolve/clear/ack retry
ordering. Physical drivers and SQL callbacks in these tests are simulated.

The controller's committed-grant recovery previously compared an immutable
original gateway operation with the current rebound gateway operation. A
two-transfer adapter regression reproduced the resulting refusal (0.703s,
`Rig/temp/m3-controller-effective-recovery-red.jsonl`). Recovery now separately
checks unchanged committed claim/proof and the fresh authorized effective
profile, current source, complete transfer-chain digest and terminal receipt.
Native recovery retains its original-operation rule and cannot accept a
transferred profile through that compatibility path. No committed SQL history
is rewritten and no new disable approval is synthesized.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/controller -run '^TestLAN(GrantCommittedRecoveryUsesEffectiveAuthority|AppGrantRecovery|AppGrantCommittedReplay)'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/controller -run '^TestLAN'
go vet -mod=readonly ./internal/controller
go build -mod=readonly -buildvcs=false ./...
```

Focused tests passed in 6.635s: four top-level tests and eleven subcases, with
no failures or skips. Cases cover native recovery, two transfers, missing or
changed proof/source/operation/tip/receipt, broken chain, changed authorized
claim, demoted approver, and existing session/disable-intent restrictions.
All selected LAN controller tests passed in 45.077s with no failures or skips;
vet and full build passed. Logs: `Rig/temp/m3-controller-effective-recovery.jsonl`
and `Rig/temp/m3-controller-lan-preservation.jsonl`. The new test uses real SQL
committed claims and projected successor authorization/runtime DTOs; it does
not establish actual SQL rebind or Docker acceptance.

Controller admission mapping confirms that recovery pinned to a committed
grant currently supports republishing only after current authorization is
restored. Normal disable creation/resumption requires an unarchived app and
current access head, while grant-pinned recovery cannot create a new disable.
Permanent stale authority must therefore remain physically withdrawn (or the
exact owned gateway stopped) with committed history retained and normal
startup refused. A future explicit approved-disable continuation requires a
separate recovery-specific authorization contract; automatic rollback of a
committed grant is not permitted.

Review tightened the native compatibility path after `48af0f0`: native SQL
authority must explicitly select the exact upgrade operation/profile, and any
nonzero physical proof must match the complete resolution. An empty SQL source
is refused. Native positive tests now create a real committed upgrade first;
missing authority and malformed nonzero proof are separate refusal cases.
The focused recovery/replay/terminal-failure run passed five top-level tests
and fourteen subcases in 8.685s with no failures or skips
(`Rig/temp/m3-controller-effective-recovery-strict.jsonl`). This supersedes
the initial native fallback in `48af0f0`.

A second boundary review required the native authorization's raw profile to
match the immutable claim, not just the effective profile/source summaries.
Recovery now checks those exact raw identity fields and recomputes both profile
digests. An internally consistent substituted native profile is rejected.
The final focused adapter test passed one top-level test and fifteen subcases
in 7.560s with no failures or skips
(`Rig/temp/m3-controller-effective-recovery-raw-binding.jsonl`).

Independent frozen review accepted `25fd1ca`: the adapter test and all fifteen
subcases passed in 7.396s, with no failures or skips
(`Rig/temp/cross-store-review-25fd-controller-recovery.jsonl`).

### Singular current-generation startup quarantine

Startup quarantine now selects the current generation for grant-only and
combined disable recovery. It derives an exact withdrawal transition from the
validated complete census, retains the raw grant and transfer evidence in the
pending marker, and rechecks the complete rebind SQL snapshot and exact
protected state before and after effects. It never resolves a SQL claim or
clears a marker. Already-unpublished prepared grants remain unchanged. A
request-bound pending grant is restored to its absent endpoint; committed
grants and disables are withdrawn. Cancellation after withdrawal starts does
not abandon that work. Failed or ambiguous effects attempt a separately
bounded stop of the exact owned gateway, retain evidence, and refuse startup.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayCurrentLANStartupQuarantine'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(GatewayCurrentLANStartupQuarantine|GatewayV2LANStartup|GatewayV2LANDisableStartup)'
go vet -mod=readonly ./internal/generatedingress ./internal/controller ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The initial focused run passed two top-level tests and two subcases in 83.955s,
with no failures or skips (`Rig/temp/m3-current-lan-startup-quarantine.jsonl`).
It checks transferred committed history, cancellation after physical effects,
replay without revision change, owned stop after apply failure, SQL drift and
protected-state replacement after withdrawal, exact disable/grant markers,
and immutable history readback. Vet and full build passed.

These are real protected-storage tests using projected SQL and simulated
physical attestations. Initial census/attestation failures can still refuse
before withdrawal; their outer startup emergency stop needs the pending real
current-generation driver integration. Refusal alone does not establish that
traffic stopped. Multi-operation batches, full retained-history validation,
and actual Docker/restart acceptance remain unfinished. No standalone PR
readiness or production acceptance is claimed for this checkpoint.

The final expanded run, including the already-unpublished prepared-grant
case, passed 24 top-level tests and 66 subcases in 128.125s, with no failures
or skips (`Rig/temp/m3-current-lan-startup-quarantine-preservation.jsonl`).
It preserves native startup/grant/disable behavior alongside the new current
path. Documentation workflow validation passed. The sandboxed docs build
could not resolve an existing Vite dependency junction; the same command
with access to the installed dependencies passed in 3.23s. No dependency
or lockfile change was needed. Commands: `pnpm --dir docs check:workflow`
and `pnpm --dir docs build`.

Independent frozen review accepted singular quarantine at `ee8f3b5`. The two
top-level tests and two subcases passed in 77.894s with no failures or skips
(`Rig/temp/cross-store-review-ee8-startup-quarantine.jsonl`). All physical and
batch limitations above remain delivery gates.

### Pure batch census binds cleared items to the retained manifest

The current batch census compares exact immutable queue requests to validated
SQL grant/disable claims, enforces head-aware terminal/clear-ack ordering, and
requires the complete same-snapshot transfer manifest to match its protected
digest. Once a transferred binding is cleared, its retained projection must
still select the exact manifest row and linked chain tip. Native grants created
under the current profile require no transfer row, but retain exact raw profile
identity. Committed grants cannot become automatic rollback items. The helper
is read-only and is not yet connected to the pending physical batch consumer.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayCurrentLANRecoveryCensusBindsClearedHistoryAndOrderedHead$'
go vet -mod=readonly ./internal/generatedingress
go build -mod=readonly -buildvcs=false ./...
```

The first focused test and its subcase passed in 16.778s, with no failures or
skips (`Rig/temp/m3-current-lan-batch-census.jsonl`). It covers two ordered
disables, current terminal replay, clear/ack boundaries, completed head history,
native and transferred bindings, missing/duplicate manifest rows, a fully
rehashed false transfer tip after clearance, out-of-order rollback, and refusal
to roll back committed grants. Vet and full build passed. Unrelated terminal
history remains non-serving census data and still requires the separate
protected historical-proof gate; this helper cannot replace that gate.

An independent negative test at `ee8f3b5` also confirmed that the existing
stored-batch structural validator accepts duplicate ports or allocations across
two unpublished grant items (17.221s, no skips;
`Rig/temp/cross-store-review-ee8-batch-uniqueness-red.jsonl`). The pure census
rejects those collisions, but the structural validator must also be corrected
before activating physical batch recovery. That correction is pending in its
separate owner scope.

The final census run added exact raw-identity mismatch, an omitted unresolved
claim, and duplicate port/allocation cases. It passed the named test and its
subcase in 17.320s with no failures or skips
(`Rig/temp/m3-current-lan-batch-census-final.jsonl`). These pure comparisons do
not prove SQL mutation, physical withdrawal, head advancement, or retirement.

Independent review accepted frozen `86cd209`: the named census test and its
subcase passed in 15.101s with no failures or skips
(`Rig/temp/cross-store-review-86cd-batch-census.jsonl`). The helper remains
unwired pending the reviewed physical batch contract. A fresh GitHub read
also confirmed draft PR #136 remains open at `9694b4e`, with 36 reported
checks, none pending or failed; this hosted evidence applies only to that
published branch, not these local recovery checkpoints.

### Complete runtime-head authorization and live-gate assertions

The reviewed shared DTO at `ae386ca` was integrated in `1a764c1`; reviewed
enforcement at `d98457d` was integrated in `3475a0b`. Approval now binds the
complete ordered application runtime-head census, including loopback-only
applications, its version, count and digest. Admission compares that census
inside the SQL writer transaction, retains immutable ordered rows, and checks
it again during guarded transition. This closes the reproduced gap where a
loopback-only application's runtime head could be added after approval and
before claim admission. Existing post-claim mutation fences were already
present; this evidence does not characterize them as bypassed.

Independent verification of frozen `d98457d` passed five top-level tests and
twelve subcases in 6.482s, with no failures or skips
(`Rig/temp/cross-store-review-d984-runtime-head-guard.jsonl`). The unchanged
earlier negative regression now passes: the unchanged census is admitted and
a post-approval loopback-head addition is refused. Boundaries also cover
deletion, redeployment, timestamp drift, zero-head nil/empty normalization,
sealed retained history and changes before or during the commit guard.
Independent security review found no concrete SQL/migration regression within
the ordinary SQL threat boundary. The owner ran the complete database and
appaccess packages in 19.885s and 107.473s. Both migration 035 mirrors have
SHA-256 `7CEE20636C4416C8A42A792C824A0D47FB67C7816E4DB3B49FEBE308687EF33B`;
previously published migrations 026, 033 and 034 are unchanged.

The two existing live cross-store Docker gates now require the signed spec to
cover both the LAN application and the loopback-only application. The process
exit/recovery gate also requires the prepared active claim and retained SQL
history to contain those exact heads. These assertions use the real two-app
fixture already selected by `hosting-gateway-v2-ci.yml`.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 ./internal/appaccess -run '^TestGatewayRebind(V2CanonicalTypes|RuntimeHeadsV2Digest)'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 ./internal/generatedingress -run '^TestLiveGatewayRebindCrossStore'
go vet -mod=readonly ./internal/appaccess ./internal/generatedingress ./internal/controller ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

At the shared DTO checkpoint, the contract tests passed three top-level tests
and four subcases in 0.521s, with no failures or skips
(`Rig/temp/m3-runtime-head-contract.jsonl`). The live package compiled in
0.836s; all three entries explicitly skipped because this Windows environment
lacks the disposable Linux Docker fixture and opt-in flags
(`Rig/temp/m3-runtime-head-live-gate-opt-in.jsonl`). Vet and the full build
passed after enforcement integration. These skips establish no Docker
acceptance. The proposal producer and typed coordinator still require their
separately owned update to pass and retain the complete census; strict
enforcement intentionally refuses the previous incomplete constructors until
that update lands. No full-unit readiness or hosted result is claimed.

Bounded independent source review accepted the live assertion delta at
`577ee04`, including its surrounding nil and history-length guards. The
integrated producer dependency was then reproduced directly:

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestInspectGatewayRebindProposalBuildsTypedTransferAwareSource$'
```

That existing test failed in 15.969s with `route_reconciliation_required`
at proposal construction, with no skips
(`Rig/temp/m3-runtime-head-producer-integration-red.jsonl`). This is the
known intermediate strict-spec/old-producer mismatch, not a passing integrated
baseline. The unchanged test must pass after the producer update lands.

### Reviewed typed history and coordinator integration

Reviewed runtime checkpoint `007397af` was integrated locally in `ecd9ecd`.
It retains the actual typed predecessor, structured effect evidence, terminal
receipt and current-state baseline rather than converting them to native
upgrade records. The canonical source union distinguishes native upgrades,
legacy rebind receipts and typed rebind receipts. The private coordinator
binds SQL transitions to retained protected evidence, exact current state and
fresh terminal observation. The production physical adapters and early startup
recovery are still separate unfinished delivery gates.

Independent checks recorded by the integration reviewer:

| Frozen checkpoint | Scope | Result |
| --- | --- | --- |
| `f6319bc` | Receipt-summary substitution, state drift before fence release and batch uniqueness regressions | 3 top-level tests, 2 subcases, 55.689s |
| `f6319bc` | Typed semantic history, empty runtime-head progress, retained local port, commit/no-effect/rollback and strict proposal producer | 7 top-level tests, 9 subcases, 153.987s |
| `1908fb6` | Fresh prepared replay with empty SQL slices and protected drift after successful terminal SQL commit | 2 top-level tests, 2 subcases, 59.592s |
| `007397af` | Canonical empty envelope reproducer, 17-record progress, terminal store/scan and empty-transfer baseline install/read | 2 top-level tests, 38.811s |

All listed runs had zero failures or skips. Logs are
`Rig/temp/cross-store-review-f631-independent-boundaries.jsonl`,
`Rig/temp/cross-store-review-f631-typed-history.jsonl`,
`Rig/temp/cross-store-review-1908-recovery-boundaries.jsonl` and
`Rig/temp/cross-store-review-0073-empty-terminal-green.jsonl`.
The empty no-effect envelope was first reproduced failing on `331838a` in
15.796s (`Rig/temp/cross-store-review-3318-noeffect-empty-red.jsonl`), then
corrected by canonical normalization shared by the prepared evidence writers.
This keeps empty SQL result sets and protected JSON representations equivalent
without changing v1 bytes. SQL coordinator tests use real SQLite and simulated
physical drivers; these results are not Docker acceptance.

### Retained LAN provenance is checked before and after physical inspection

Current-generation startup and singular quarantine now validate the complete
retained transfer chain against the same SQL snapshot, then bind every link to
its real protected source and immutable activation manifest. Native grants
without transfers are tied to the actual native or rebound profile lineage.
Historical sources are never installed as current authority, and old mutable
application routes are not treated as evidence of past clearance. Startup
requires retained authority for a committed grant-based disable after rebind;
omitting the projection cannot select the old no-authority compatibility path.

The first executable regression returned normal startup for four structurally
valid but unproved histories: a rehashed false transfer, rehashed false receipt,
unknown protected source and omitted complete chain. This RED run took 19.732s
(`Rig/temp/m3-retained-history-red.jsonl`). It used an overlay that replaced only
the new, then-unwired helper with an empty package so the unchanged public
startup consumer could be tested before the typed-history dependency landed.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^Test(GatewayCurrentLANStartupRequiresRetainedHistoricalProof|GatewayCurrentLANRetainedHistoryUsesTypedTerminalAndRechecksOldOrigin|InspectGatewayRebindProposalBuildsTypedTransferAwareSource)$'
go build -mod=readonly -buildvcs=false ./...
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 ./internal/generatedingress -run '^TestLiveGatewayRebindCrossStore'
```

The integrated run passed three top-level tests and seven subcases in 68.930s,
with no failures or skips (`Rig/temp/m3-retained-history-and-producer.jsonl`).
It adds missing-projection refusal, native historical positives under both
original and rebound profiles, and a two-transfer chain through real protected
legacy and typed terminal files. Replacing an older generation's protected
manifest after current physical inspection is refused without overwriting the
changed evidence or the current generation. The previously failing proposal
test also passed unchanged. These historical tests use projected SQL snapshots
and simulated physical attestations; they do not establish actual withdrawal,
SQL disable mutation, or container restart behavior.

The full build passed. The updated live tests additionally compare the proposal's
retained runtime-head list with the real SQL census. Their integrated local run
compiled in 0.766s and explicitly skipped all three opt-in entries
(`Rig/temp/m3-runtime-head-live-gate-integrated-opt-in.jsonl`); actual Docker
behavior remains unverified. Vet identified a copied manager lock in the new
prepared-replay test fixture, which is being corrected separately. No complete
M3 readiness or publication approval is claimed by this checkpoint.

Existing current startup/quarantine preservation also passed four top-level
tests and sixteen subcases in 111.591s, with no failures or skips
(`Rig/temp/m3-retained-history-startup-preservation.jsonl`):

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayCurrentLANStartup(Quarantine|CensusBindsRawGrantAndRecovery|PublicReadRechecksAuthority)'
```

Independent code/security review accepted frozen `ee88319`. Both new historical
tests and all seven subcases passed independently in 56.353s with no failures
or skips (`Rig/temp/cross-store-review-ee883-retained-history.jsonl`). This is
provenance validation only; physical serving and historical clearance remain
separate obligations.

The copied-lock fixture correction `44f24397` was cherry-picked alone as
`7f4828a`; its held emergency-stop ancestry was not imported. The corrected
test constructs a new manager and mutex while retaining only the durable
store and options. Its owner replay test passed one top-level test and two
subcases in 38.420s. Integrated vet and full build now both pass:

```powershell
go vet -mod=readonly ./internal/generatedingress ./internal/appaccess ./internal/controller ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

Focused compatibility checks after the shared batch/typed-format changes also
passed, with no failures or skips:

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayCurrentLANRecovery(BatchTransitionsPreserveQueue|CensusBindsClearedHistoryAndOrderedHead)$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^Test(GatewayRebindAttemptViewPreservesLegacySourceWithoutChangingFormat|GatewayCurrentPreservesTerminalV1CanonicalShapeAndDigest|GatewayRebindProgressOptional.*PreservesSequence.*BytesAndDigests)$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 ./internal/appaccess -run '^TestGatewayRebindV1CanonicalDigestsRemainStable$'
pnpm --dir docs check:workflow
pnpm --dir docs build
```

The batch state/census run passed two top-level tests and one subcase in
33.141s (`Rig/temp/m3-reviewed-batch-contract-preservation.jsonl`). Five
protected-format tests passed in 20.601s
(`Rig/temp/m3-typed-integration-v1-preservation.jsonl`), and the SQL v1 digest
golden passed in 0.277s (`Rig/temp/m3-typed-integration-v1-sql-golden.jsonl`).
Documentation workflow validation and its production build passed, the latter
in 3.80s using the installed dependencies. Runnable batch withdrawal, exact
emergency stop, early startup recovery and repeated/crash Docker acceptance
remain unfinished. No local Docker or Windows race result is claimed.

## Protected withdrawal ownership integration

Reviewed runtime commit `748951292b548b24d42d584ce6afe332de622a95` was integrated
locally as `1d8edbe`. It adds protected-only withdrawal targets containing the
exact canonical terminal, generation-scoped state, final container identity,
and (when present) the complete Before/Pending/Effective transition. A caller
cannot expand the permitted state-digest set without that exact transition.
Revalidation reloads the state and terminal at the effect boundary. Enumeration
rejects missing/corrupt route bundles and ambiguous final-container ownership.
These targets authorize ownership checks for withdrawal only; they do not
select SQL current or authorize serving.

Independent review initially reproduced two failures against `eabcd`: arbitrary
permitted digests were accepted, and an unreadable route bundle was skipped.
The unchanged two reproducers plus transition/missing-bundle checks passed
against `7489512`: four top-level tests in 68.271s, with no failures or skips
(`Rig/temp/cross-store-review-748-owned-stop-green.jsonl`). The original red
run is retained in `Rig/temp/cross-store-review-eabcd-owned-stop-red.jsonl`.
Independent code/security review accepted this helper boundary only.

The integration does not add an executable physical stop or a hostd emergency
API. A committed terminal without its baseline still requires phase recovery;
refusal at that boundary is not evidence that traffic has stopped. No Docker,
complete startup recovery, publication, or M3 completion is claimed.

## Repeated commit and rollback sequence verification

`gateway_rebind_cross_store_sequence_test.go` now exercises real SQLite and
protected-file coordination across native → committed rebind → another
committed rebind, plus both no-effect abort → commit and post-intent rollback →
commit. The sequences verify exact prior-transfer links, original raw
allocation/access revision/grant/profile equality, immutable prior SQL history,
unchanged retained file identity/content/mode/time, generation advancement,
current selection and fence release. The repeated path also reads the complete
startup census and enumerates both generations' protected withdrawal targets.
Simulated ingress networks, stage/final containers and volume paths are distinct
per attempt; application-network identities remain those of the unchanged apps.

The initial sequence run passed two top-level tests and two subcases in 95.510s,
with no failures or skips (`Rig/temp/m3-cross-store-sequences-initial.jsonl`):

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayRebindCoordinator(CommitsRepeatedTransferChain|CommitsAfterRetainedRollback)$'
go vet -mod=readonly ./internal/generatedingress
```

Vet also passed. Docker observations and effects remain explicitly simulated;
these checks do not prove container changes, traffic withdrawal, or process
restart. A follow-up variant gives the second successor a distinct address and
interface. Its first run correctly rolled back because the synthetic host
inventory still lacked that address (27.074s,
`Rig/temp/m3-cross-store-sequences-distinct-profile.jsonl`); this was a fixture
mismatch, not a production regression. The test inventory was then updated to
describe the new candidate and interface prefix.

The corrected distinct-address sequence passed in 39.497s, with no failures or
skips (`Rig/temp/m3-cross-store-sequences-distinct-profile-green.jsonl`):

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayRebindCoordinatorCommitsRepeatedTransferChain$'
go build -mod=readonly -buildvcs=false ./...
```

The integrated full build passed. Remaining physical/startup/batch gates above
are unchanged, and no new branch has been published.

Independent QA/code review accepted frozen `1682f09`. Its complete sequence run
passed two top-level tests and two subcases in 100.075s, with no failures or
skips (`Rig/temp/cross-store-review-1682-sequences.jsonl`). This run includes
the distinct second address/interface and both rollback paths.

Startup integration must respect the existing effects lease: `main.go` acquires
ordinary worker admission before gateway inspection, and that admission already
rejects an active rebind fence. The planned lease-owning rebind recovery API must
therefore run immediately after opening the database, before ordinary admission.
Ordinary admission must then reacquire its lease and read a fresh fence/census.
Invoking recovery beneath the held startup lease would nest a nonreentrant lock.
Emergency stop must likewise have an explicit lease policy for failures that
occur while startup admission is still held. These integration requirements are
confirmed with the runtime owner; the executable API is not yet available.

## Ordinary redeploy between rebinds

The additional `TestGatewayRebindCoordinatorPreservesTransferAcrossOrdinaryRedeploy`
sequence commits a first rebind, redeploys the same immutable release through
the real generated-runtime repository and public `Manager.Switch`, and commits
a second rebind. The redeploy holds the effects lease, advances the ordinary
runtime phases/components, publishes a protected green-slot route, and calls
the real `SwitchActive` transaction before completing the job/deployment.
Only the Docker start, health and publication observations are simulated.

The second approval and SQL history must retain the exact new complete runtime
heads and updated protected source digest. The second current must preserve the
new app route while the original allocation/access revision/grant/profile and
first transfer remain unchanged. Prior protected history and SQL rebind history
are compared again after the second commit. This closes a sequence coverage gap
between the previously separate ordinary-switch and repeated-rebind tests.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayRebindCoordinatorPreservesTransferAcrossOrdinaryRedeploy$'
go vet -mod=readonly ./internal/generatedingress
```

The new test passed in 43.109s with no failures or skips
(`Rig/temp/m3-cross-store-redeploy-sequence.jsonl`); vet and `git diff --check`
also passed. This is local SQL/protected-state acceptance, not a real Docker
redeploy, a process-restart result, or complete M3 acceptance.

Independent QA/code review accepted frozen `a696eda`. The exact redeploy test
passed independently in 42.755s with no failures or skips
(`Rig/temp/cross-store-review-a696-redeploy-sequence.jsonl`). The scope remains
real SQL/protected/ordinary-switch coordination with simulated Docker effects.

## Whole-batch physical contract and current head observation

Reviewed runtime commits `05810a7` and
`b980f6bcce0dd4c31b267fa76f5f3c12db12d013` were integrated locally as `c8f3ba9`.
The contract binds the complete selected queue, canonical terminal, before
projection and all-items withdrawal projection. Partial physical withdrawal is
represented explicitly with `BatchAbsent=false` and cannot authorize queue
dispatch or SQL clearance. The completed queue has `Complete=true`, an empty
head and unchanged logical clearance state; it can be attested but cannot
invoke another withdrawal. No app route is manufactured for a missing app.

Independent review reproduced the initial completed-queue failure at `05810a7`
in 16.980s (`Rig/temp/cross-store-review-058-completed-batch-red.jsonl`). The
unchanged regression plus five focused contract tests passed at `b980f6b`:
six top-level tests, no failures/skips, 102.087s
(`Rig/temp/cross-store-review-b980-batch-contract-green.jsonl`). This accepted
the canonical contract, not the managed Docker adapter; its default factory
remains closed.

The public `ObserveGatewayV2LANRecoveryHead` now uses the selected rebind
generation before considering a native owner. Its current path validates the
complete supplied SQL census and retained historical sources, requires a whole
batch absence proof, and rereads exact SQL authority and protected state after
physical inspection. A completed queue returns `Head==Count` with no operation
identity. The reader never clears a binding, advances a head, writes SQL, or
performs withdrawal.

The initial unsupported-current regression returned an unresolved error with
`present=false` (17.375s, `Rig/temp/m3-current-batch-observer-red.jsonl`). With
the current reader, a selected batch whose driver lacks batch proofs stays
explicitly present and unresolved. New tests also cover sequential observation
through completion, valid partial-withdrawal refusal, omitted unresolved claims
before physical inspection, cancellation, SQL drift and protected-revision
drift after proof. Deliberately changed evidence is retained on refusal.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayCurrentLANRecoveryHead'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayV2LANRecovery(FinalizesTwoDisablesSequentiallyAndRetires|InspectorReprovesProcessedPrefixPorts|InspectorRejectsCommittedStaleGrant)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The current observer suite passed three top-level tests and five subcases in
56.227s (`Rig/temp/m3-current-batch-observer-green.jsonl`). Native preservation
passed three top-level tests in 2.535s
(`Rig/temp/m3-current-batch-observer-native-preservation.jsonl`). Both runs had
zero failures/skips; vet, full build and `git diff --check` passed. The observer
tests use protected files with projected SQL snapshots and simulated physical
proofs. Actual whole-batch withdrawal, ordered callback/finalization writes,
retirement, startup dispatch and Docker restart acceptance remain open.

Independent review accepted frozen `814e072`: the three observer tests and five
subcases passed in 59.973s with zero failures/skips
(`Rig/temp/cross-store-review-814-current-batch-observer.jsonl`). Source/security
review found no issue in this read-only scope. Claim freshness still depends on
the caller's existing complete Hosting snapshot and pin rechecks.

## Completed current batch retirement

The public retirement regression first failed at `814e072` with
`route_reconciliation_required` (one test, 17.424s;
`Rig/temp/m3-current-batch-retirement-red.jsonl`). The new current consumer
requires the complete terminal/acknowledged census, retained history, and a
fresh serving withdrawal proof before persisting a fence-free next revision.
It then reattests the exact remaining topology and reconfirms SQL/protected
authority. An already stopped gateway retains its completed batch for recovery;
port absence alone does not authorize retiring that marker.

Uncertain proof/write outcomes latch ordinary admission and attempt withdrawal
using only an exact captured protected owner. The optional state-only stop
driver has no SQL dependency, cannot manufacture an ordinary transition, and
must prove the exact container and listeners stopped/absent. Its Manager wrapper
revalidates the protected target before and after the physical stop. Concrete
Docker execution is a separate implementation and remains subject to review.

An explicit full-unit acceptance case remains: when failure happens after the
retirement revision was durably written, compensation can leave a stopped
gateway with no pending marker or batch. Fresh-process recovery must reauthorize
and restore that exact remaining topology. A process-scoped latch alone does
not establish restart recovery; the retirement unit must not be used to claim
that result. It does not rewrite old history or synthesize a recovery marker
while SQL is unreadable.

Owner verification used these exact commands:

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayCurrentLANRecovery(RetiresCompletedBatch|Retirement)'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayCurrentLANRecoveryRetirementStopsExactOwnerOnLostProof$/^stop_failure$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(GatewayCurrentLANRecovery(RetiresCompletedBatch|RetirementRetainsStoppedBatch)|GatewayCurrentOwnedStopDriverUsesProtectedAuthorityAndRejectsDrift|GatewayV2LANRecovery(FinalizesTwoDisablesSequentiallyAndRetires|InspectorReprovesProcessedPrefixPorts|InspectorRejectsCommittedStaleGrant))$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The initial combined run passed three top-level tests and five failure subcases,
then timed out at its four-minute limit while constructing the final stop-failure
fixture (240.386s, `Rig/temp/m3-current-batch-retirement-green.jsonl`; despite its
filename, this is a timed-out run, not an overall pass). The exact uncompleted
case passed separately in 38.036s, one top-level test and one subcase, zero
failures/skips (`Rig/temp/m3-current-batch-retirement-stop-failure.jsonl`).

After adding the stopped-before-retirement guard from review, the final-source
run passed six top-level tests and four subcases in 101.451s, zero failures/skips
(`Rig/temp/m3-current-batch-retirement-final-source.jsonl`). It covers successful
retirement, retaining a stopped batch, protected-only stop through SQL failure
and the admission latch, unsupported capability refusal, stale/drifting ownership,
and three native recovery regressions. Vet, full Go build, gofmt and
`git diff --check` passed. These are protected-file tests with projected SQL and
simulated physical evidence; they do not establish real Docker stop or restart.

Independent review accepted frozen `df9689a`. The exact positive retirement,
stopped-marker preservation and ownership-wrapper tests passed independently:
three top-level tests and four subcases, zero failures/skips, 111.105s
(`Rig/temp/cross-store-review-df968-retirement-guard.jsonl`). This bounded review
does not close the documented fresh-process stopped/no-marker recovery case.

## Current whole-batch quarantine

The public current-generation quarantine regression first failed at `df9689a`
with `route_reconciliation_required` (one test, 28.537s;
`Rig/temp/m3-current-batch-quarantine-red.jsonl`). The current consumer now derives
and persists one immutable queue from the complete validated claims before
withdrawing every unsafe binding. Replay keeps the same revision and head;
the completed queue is reattested without invoking a head effect. Existing
ordinary pending evidence is carried in `LegacyPending`.

Unqueueable claims, including a stale committed grant, stop the exact protected
owner before recovery refusal; they never fabricate a rollback or disable.
Failures after intent retain the queue and compensate through the protected
state-only stop path even when SQL becomes unreadable. Once quarantine starts,
a bounded independent context completes withdrawal or compensation despite
client cancellation. Every physical proof/effect is followed by exact SQL,
protected-state and retained-history confirmation. SQL claims are never resolved
by this consumer; ordered explicit finalization remains separate work.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=6m ./internal/generatedingress -run '^TestGatewayCurrentLANRecoveryBatch(Quarantine|Quarantines|StopsUnqueueable|RetainsIntent)'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^Test(GatewayCurrentLANRecoveryHeadObservesWholeBatchAndCompletion|GatewayV2LANRecoveryBatch(QuarantinesTwoPreparedDisables|RejectsCommittedStaleGrantBeforeIntent|StopsOwnedGatewayOnInvalidReplayCensus))$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
```

The new quarantine suite passed five top-level tests and five subcases in
254.078s, zero failures/skips (`Rig/temp/m3-current-batch-quarantine-green.jsonl`).
It verifies all-items withdrawal after durable installation, replay without a
new revision/effect, pre-intent refusal with owned stop, lost withdrawal
acknowledgement, SQL failure after withdrawal, bounded completion despite client
cancellation, completed-queue preservation and retained pending evidence.
The tests use real protected files with projected SQL and simulated physical
effects. They do not verify the actual managed Docker adapter.

Preservation passed four top-level tests with zero failures/skips in 21.286s
(`Rig/temp/m3-current-batch-quarantine-preservation.jsonl`): the shared current
observer fixture and three native quarantine success/refusal paths. Vet, gofmt
and `git diff --check` also passed.

## Startup recovery coordinator integration

Independent review also accepted quarantine `d14c0ae`: three top-level tests
and five subcases passed in 108.951s, zero failures/skips
(`Rig/temp/cross-store-review-d14-quarantine.jsonl`). The exact selection was
`^TestGatewayCurrentLANRecoveryBatch(QuarantinesAllItems|StopsUnqueueableClaimsBeforeIntent|RetainsIntentAfterEffectUncertainty)$`.
Source/security review found no blocker within this consumer's scope.

Reviewed `99d7cc8` is integrated at `a1a54d3`. The public
`RecoverGatewayRebindStartup` coordinator owns effects and gateway leases,
recovers phase-bound SQL/protected intent, reproves no-effect abort receipts,
and reattests committed current authority before releasing the fence. A
database-committed operation permits forward recovery only. Required lease
release failures latch admission closed, including the no-history path.

Independent execution passed six top-level tests and three subcases in
203.837s, zero failures/skips
(`Rig/temp/cross-store-review-99d7-startup-recovery.jsonl`). A separate
checkpoint-removal overlay passed in 18.917s and preserved the prepared fence:
the suspected first-receipt freshness gap did not reproduce and is not a
validated vulnerability (`Rig/temp/cross-store-review-99d7-first-noeffect-freshness.jsonl`).

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^Test(RecoverGatewayRebindStartupNoHistoryOwnsAndReleasesAdmissionLocks|GatewayRebindCoordinatorPreservesTransferAcrossOrdinaryRedeploy)$'
go build -mod=readonly -buildvcs=false ./...
```

After integration, both named tests passed in 37.168s, zero failures/skips
(`Rig/temp/m3-current-batch-integrated-recovery.jsonl`), and the full Go build
passed. These tests simulate physical effects. The production typed Docker
adapter remains unavailable; startup dispatch must still be wired before
ordinary admission with the protected emergency stop path. This is not
evidence of actual Docker recovery or a completed M3 milestone.

## Ordered current batch finalization

The current-generation public finalization regression first failed at
`9c61784`: its exact disable head never reached the SQL callback (one test,
15.727s; `Rig/temp/m3-current-batch-finalization-red.jsonl`). Current finalization
now uses the same locked public dispatch as native recovery. It accepts only
the exact immutable head, proves every queued port withdrawn, invokes the
authorized terminal callback, clears the protected binding, invokes the
disable clear acknowledgment, then advances one head. The completed queue
stays installed until the independent retirement census succeeds.

Every proof/callback/write boundary reconfirms selected SQL rebind authority,
protected state and retained history. SQL callbacks remain responsible for
actor authorization, exact terminal state and durable readback. Callback
errors preserve the current head for replay, including a lost acknowledgment
after SQL success. Physical or protected uncertainty latches admission closed
and attempts bounded exact-owned withdrawal, without requiring SQL. A grant
that never published uses its immutable raw head and actual protected digest;
it does not manufacture a serving binding or a revision-only clearance.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=7m ./internal/generatedingress -run '^TestGatewayCurrentLANRecovery(Finalizes|Finalization)'
```

The initial suite passed four top-level tests and four subcases in 144.177s,
zero failures/skips (`Rig/temp/m3-current-batch-finalization-green.jsonl`). It
covers sequential two-app disable completion through retirement, immutable
history, refusal of later/old heads, callback failure and cleared replay,
partial withdrawal, SQL loss, cancellation, protected drift, and an unpublished
grant. These use real protected files, projected SQL claims and simulated
physical proofs; they do not establish the controller/database/Docker journey.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(GatewayCurrentLANRecoveryFinalizationPreservesAmbiguousWrite|GatewayV2LANRecovery(FinalizesTwoDisablesSequentiallyAndRetires|FinalizesMixedGrantDisableWithoutChangingUnrelatedRoute|DisableAcknowledgmentReplayAdvancesOnce|RejectsCrossItemAndHeadSkip))$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The final additional suite passed five top-level tests and two subcases in
40.584s, zero failures/skips
(`Rig/temp/m3-current-batch-finalization-write-preservation.jsonl`). It injects
an error after each actual protected clearance/head-advance write, checks exact
installed evidence and owned-stop compensation, and preserves four native
finalization paths. Vet, the full Go build, gofmt and `git diff --check` passed.

## Reviewed Docker driver and recovery checks

Independent source/security review accepted finalization `232ee4d`. Public
sequential completion, callback replay/head guard, and ambiguous clearance/head
write cases passed three top-level tests and two subcases in 105.325s, zero
failures/skips (`Rig/temp/cross-store-review-232ee-finalization.jsonl`), using
`^TestGatewayCurrentLANRecovery(FinalizesTwoDisablesInOrder|FinalizationReplayAndHeadGuard|FinalizationPreservesAmbiguousWrite)$`.
The review found no new blocker in this bounded consumer; complete startup
census/pin and SQL callback authorization remain required.

With explicit user authorization for these two local integrations, reviewed
`273769b` (including `47272c9`) was merged at `9cdf800`, followed by reviewed
`fab0fbd` at `ce56b9d`. No remote branch or PR was changed.

The managed current driver now has concrete ordinary reconciliation and
protected-only owned-stop implementations. Independent executor tests passed
four top-level tests and five subcases in 146.155s. Review then reproduced an
extra ingress network peer acceptance bug (14.053s); the corrected exact
membership guard and owned-stop preservation passed three top-level tests
and three subcases in 70.525s. Logs:
`Rig/temp/cross-store-review-47272-current-physical-executor.jsonl`,
`Rig/temp/cross-store-review-47272-ingress-peer.jsonl`, and
`Rig/temp/cross-store-review-273769-ingress-green.jsonl`.

The recovery follow-up reconfirms exact SQL, history, checkpoint and protected
files after no-effect proof and before SQL rollback. Independent positive and
snapshot-drift refusal tests passed two top-level tests in 32.742s, zero
failures/skips (`Rig/temp/cross-store-review-fab0-noeffect-reconfirmation.jsonl`).

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(GatewayCurrentLANRecoveryFinalizesTwoDisablesInOrder|ManagedGatewayCurrentPhysicalRuntimeAttestationRequiresExactIngressMembership|RecoverGatewayRebindStartupRefusesFirstNoEffectAbortAfterSnapshotDrift)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

Parent integration passed three top-level tests and three subcases in 67.259s,
zero failures/skips (`Rig/temp/m3-current-batch-integrated-driver.jsonl`). Vet
and the full Go build passed. Executor simulations are not actual Docker
acceptance. The production factory remains closed pending the batch adapter,
typed rebind recovery adapter, protected emergency startup path and guarded
restart of stopped current state. Hosted Docker and Linux race gates remain
unverified for this unit.

## Real SQL disable after typed rebind

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayCurrentLANRecoveryFinalizationCommitsRealSQLAfterRebind$'
```

The integration test passed in 30.773s, one top-level test, zero failures/skips
(`Rig/temp/m3-current-batch-finalization-sql.jsonl`). It commits a typed rebind
through the actual SQLite/protected coordinator, approves and authorizes a
disable of the transferred source grant, and runs public current finalization
with real terminal SQL release/readback and durable protected-clear acknowledgment.
It then verifies the completed protected head, released allocation, exact
retained transfer chain, unchanged source approval/proof and unchanged rebind
history/current authority. Vet, gofmt and `git diff --check` passed.

Queue installation and physical withdrawal are fixture setup. This test does
not claim controller HTTP authorization, startup conversion, or real Docker
acceptance. Those remain part of the complete runtime integration gate.

Independent review accepted `fc463d8`; its exact real-SQL test passed again in
30.714s, zero failures/skips
(`Rig/temp/cross-store-review-fc463-real-finalization.jsonl`).

## Consistent startup runtime census

`HostingGatewayStartupSnapshot` now includes all active generated runtime heads
and their exact component rows in the same read transaction as gateway/LAN/rebind
authority. Loopback applications are included; archived/inactive applications
are excluded. Failed component rows remain visible for refusal by serving
authorization. Missing, malformed or crossed component identities return no
partial snapshot. No schema or immutable history format changes are involved.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/appaccess -run '^TestHostingGatewayStartupRuntimeCensus'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=5m ./internal/appaccess ./cmd/hostd -run 'Startup|RuntimeHeads'
go vet -mod=readonly ./internal/appaccess ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The new census tests passed three top-level tests and seven subcases in 4.145s,
zero failures/skips (`Rig/temp/m3-startup-runtime-census-green.jsonl`). They
exercise a concurrent redeploy through a separate SQLite handle, complete
LAN/loopback enumeration, failed component visibility, read-only behavior and
seven malformed/missing component cases. The first test run exposed an invalid
test setup: SQL correctly refused draining an actively routed component. The
fixture was corrected to the supported active-to-failed transition; no production
guard was weakened (`Rig/temp/m3-startup-runtime-census.jsonl`).

This read projection is input to the pending guarded restart consumer, not
permission to restart or evidence of Docker serving. Vet and the full build passed.

The preservation run passed 32 appaccess tests plus 64 subcases in 34.628s,
and 14 hostd tests plus 26 subcases in 3.167s, zero failures/skips
(`Rig/temp/m3-startup-runtime-census-preservation.jsonl`). Independent review
accepted `2789250`: three census tests plus seven subcases passed in 4.037s
(`Rig/temp/cross-store-review-278925-runtime-census.jsonl`).

## Guarded restoration of current serving state

`RestoreGatewayCurrentServingStartup` takes the deployment effects lease and
gateway locks before reading complete SQL authority. It accepts only a stable
current generation or a fully completed LAN recovery batch. Every protected
endpoint must match an active SQL component in the exact active deployment and
slot, including loopback applications. The complete authority digest, selected
protected state, terminal history, fence, retained LAN provenance and completed
batch acknowledgments are reconfirmed at physical effect boundaries and after
serving proof. A final selected-state read covers mutable bundles that are not
included in the immutable-history fingerprint.

Restoration does not write SQL, retire a recovery queue or rewrite history. A
failure after selection latches admission and stops the exact protected owner
using a bounded uncancelled context. Changed ownership or an unproved stop remains
explicitly unresolved. Unfinished operations stay on their separate recovery path.
The controller's existing LAN SQL conversion was moved into shared ingress
functions without changing its claims or retained authority semantics.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=6m ./internal/generatedingress -run '^TestGatewayCurrentServing(Restore|RuntimeCensus)'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=5m ./internal/generatedingress -run '^TestGatewayCurrentServingRestore(StopsExactOwnerAfterLostProof|RefusesUnfinishedBatch)$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayCurrentServingRestoreRefusesLastReadProtectedReplacement$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./cmd/hostd -run 'Startup'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The first restart run failed because the older commit fixture deliberately used
different SQL and protected container identities. The guard correctly refused it.
Fixtures now perform an ordinary redeploy through the real runtime repository and
public ingress switch before asserting restart; the ownership check was not weakened.
Initial failure logs are `Rig/temp/m3-current-serving-restore-initial.jsonl` and
`Rig/temp/m3-current-serving-restore-completed.jsonl`.

The corrected initial suite passed five tests and 14 subcases in 180.646s
(`Rig/temp/m3-current-serving-restore-green.jsonl`). Further compensation and
unfinished-batch checks passed two tests and four subcases in 129.923s
(`Rig/temp/m3-current-serving-restore-compensation.jsonl`). They cover lost SQL
authority after the effect, cancellation, a self-consistent proof for the wrong
revision, and a failed stop acknowledgment. The late protected replacement check
passed one test in 28.671s (`Rig/temp/m3-current-serving-restore-final-selection.jsonl`).
The initial regex ran before these last three tests were added; the logs do not
claim a single aggregate run. Every recorded run has zero failures/skips.

Shared projection preservation passed 14 hostd tests and 26 subcases in 3.169s
(`Rig/temp/m3-shared-lan-projection.jsonl`). Vet, the full Go build, gofmt and
`git diff --check` passed. These tests use actual SQLite/protected state with an
injected physical driver; the fixture redeploy also simulates Docker. Concrete
restore driver review, hostd startup wiring, actual Docker and Linux race gates
remain required. The production physical factory remains closed.

## Approved local Docker batch and emergency integrations

The user explicitly approved local integration of reviewed `d5ec8b9` and
`1bcc5e1` into this branch, and of previously reviewed `273769b` into the runtime
worker branch. Parent merges are `03d1127` and `7d60803`; the worker integration
is `98bb3c2`. This approval does not authorize remote publication, GitHub PR
merges or deployment. The worker's further typed adapter work remains separate.

The batch adapter initially reported whole-batch absence after a lost reload
acknowledgment while restart configuration still retained the withdrawn LAN
bindings. Independent regression reproduced that failure in 18.204s
(`Rig/temp/cross-store-review-cf709-batch-restart.jsonl`). Corrected `d5ec8b9`
requires canonical live and restart configuration to agree before reporting
complete absence. Five independent tests passed in 88.864s, zero failures/skips
(`Rig/temp/cross-store-review-d5ec-batch-green.jsonl`).

The emergency API initially missed an orphan scoped current bundle beside a
valid committed generation. Independent regression reproduced the failure in
17.266s (`Rig/temp/cross-store-review-5a786-emergency-orphan.jsonl`). Corrected
`1bcc5e1` associates every bundle with its exact protected terminal and repeats
the complete ownership census after physical withdrawal. Six checks passed in
64.838s, followed by four final checks in 37.543s, zero failures/skips
(`Rig/temp/cross-store-review-1bcc-emergency-final.jsonl`). Active typed attempts
remain explicitly incomplete until their separate physical adapter can prove
withdrawal; unknown ownership never authorizes serving or native fallback.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(ManagedGatewayCurrentLANRecoveryRuntimeWithdrawsWholeBatchAfterLostReloadAcknowledgement|GatewayCurrentStartupEmergencyStopRejectsWholeHistoryDriftAfterPhysicalStop|GatewayCurrentServingRestoreKeepsCompletedBatchAndTerminalSQL)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

Parent integration passed three tests in 79.699s, zero failures/skips
(`Rig/temp/m3-current-restart-emergency-integration.jsonl`). Vet and the full
Go build passed. Command-executor simulations and injected physical proofs are
not acceptance evidence for actual Docker.

## Controller emergency startup dispatch

The controller now releases any held startup effects admission before calling
the emergency API with a fresh bounded context. It retains a release failure
even if subsequent withdrawal succeeds. Protected current history is checked
first without SQL. The native emergency path is permitted only after complete
no-current-ownership evidence (including fully retained abort history). Current
ownership, unknown history, incomplete stops, changed census and inconsistent
counts cannot be converted into a native-only success.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./cmd/hostd -run 'Startup|RuntimeWorker'
go vet -mod=readonly ./cmd/hostd ./internal/generatedingress
go build -mod=readonly -buildvcs=false ./...
```

The controller suite passed 24 tests and 42 subcases in 3.746s, zero failures/skips
(`Rig/temp/m3-hostd-current-emergency-wiring.jsonl`). New cases verify admission
release ordering, retained release/stop failures, fresh context lifetime, twelve
current/native dispatch cases, cancellation and native ownership failure. Vet,
the full Go build, gofmt and `git diff --check` passed. These dispatcher tests
inject stop callbacks. Actual emergency Docker behavior and default driver
activation remain part of the pending complete runtime gate.

Independent review accepted the guarded serving consumer at `8ca3ada`: four
tests and two selected post-proof subcases passed in 158.053s, zero failures/skips
(`Rig/temp/cross-store-review-8ca-serving-restore.jsonl`). Source/security and
concrete-driver compatibility review found no further consumer defect. The
concrete driver's actual per-effect guard calls still require its own evidence.

## Typed history and native emergency fallback preservation

A real SQLite/protected no-effect abort fixture exposed two distinct integration
gaps. First, the presence parser recognized a typed terminal filename but loaded
it with the legacy terminal purpose/type. The initial regression failed at the
current ownership census in 18.141s
(`Rig/temp/m3-native-emergency-retained-abort-red.jsonl`). After adding the typed
terminal loader dispatch, the same test reached the native emergency path and
failed because its old history scan rejected every retained rebind artifact,
in 17.473s (`Rig/temp/m3-native-emergency-retained-abort-fallback-red.jsonl`).

Native journal selection now permits retained rebind history only after complete
protected enumeration proves no current ownership or unresolved attempt. That
census is repeated before selection returns; the exact selected journal and
absence of rebind ownership are verified again after stop. A changed history
prevents successful completion and latches admission.

The emergency presence mode preserves path safety, file fingerprints and strict
validation of all rebind/current artifacts while allowing unreadable native route
JSON when there are no rebind artifacts. This preserves the existing ability to
stop an exact journal-bound native gateway after route-state corruption. It grants
no serving authority, and the normal startup presence reader remains strict.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^Test(GatewayCurrentNativeEmergencyFallbackRetainsAbortedRebindHistory|GatewayV2LANStartupFailureStopsOnlyJournalBoundGatewayWithoutReadingRouteState)$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayCurrentNativeEmergency(Refuses|Preserves)'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(GatewayRebindStartupPresence|GatewayV2ProductionEmergencyStop|GatewayV2EmergencyStopConstruction|GatewayV2LANCommitResolutionStopsOwnedGatewayWhenProtectedRouteUnreadable|GatewayV2LANDisableAmbiguousWithdrawalFailureStopsOwnedGateway)'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The corrected positive and original native-route corruption test passed two tests
in 20.009s (`Rig/temp/m3-native-emergency-retained-abort-green.jsonl`). Additional
typed committed ownership, both native route corruption forms, strict serving
reader preservation and post-stop history drift checks passed three tests plus
two subcases in 24.468s (`Rig/temp/m3-native-emergency-typed-and-corruption.jsonl`).
Both runs have zero failures/skips. The new tests exercise actual protected history
and the typed commit/abort SQL paths with injected physical stop drivers; they do
not claim Docker process acceptance. Vet and the full Go build passed.

Existing presence and native emergency preservation passed 14 tests and 18
subcases in 20.673s, zero failures (`Rig/temp/m3-native-emergency-preservation.jsonl`).
The Windows run skipped the POSIX directory-permission test and two symlink
subcases; those remain required in the Linux gate. Gofmt and `git diff --check`
passed. The native fallback now has real retained typed history acceptance,
while its physical stop remains simulated.

Independent review of `c828fd5` plus `eba4e29` passed seven tests and sixteen
subcases, zero failures/skips (ingress 42.161s; hostd 0.753s;
`Rig/temp/cross-store-review-eba-emergency.jsonl`). The typed/native selection
behavior passed, but source review held completion for the native emergency
effect boundary: it still lacked its own deployment effects lease and used a
lock that rejects an existing process fail-stop.

Two positive-first boundary tests reproduced both defects in 1.280s
(`Rig/temp/m3-native-emergency-effects-boundary-red.jsonl`): a preexisting latch
prevented withdrawal, and a native stop bypassed an actually held effects lease.
The native emergency API now owns that lease before its gateway locks, uses the
withdrawal-only lock path, latches ordinary admission before stop, and retains
foreign commit barriers and all release errors. Callers must release their own
startup effects admission first. Existing ordinary/serving locks remain strict.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^Test(GatewayCurrentNativeEmergency(CrossesExistingFailStopOnlyToWithdraw|WaitsForActualEffectsLease|ReleaseFailureRetainsAdmissionLatch)|GatewayV2EmergencyStopConstructionBypassesUnavailableDatabaseFenceOnlyForExactOwnedStop|GatewayV2LANStartupFailureStopsOnlyJournalBoundGatewayWithoutReadingRouteState)$'
```

All five tests passed in 1.974s, zero failures/skips
(`Rig/temp/m3-native-emergency-effects-boundary-green.jsonl`), including actual
lease contention, existing latch/foreign barrier preservation, release failure,
the exported command-runner stop and corrupt native route-state withdrawal.
Fixtures isolate intentional process latching so it cannot contaminate later
tests. Vet, the full Go build, gofmt and `git diff --check` passed. Actual Docker
and Linux race acceptance remain pending.

Independent review accepted the combined controller/emergency scope at
`325a63a`, including `c828fd5` and `eba4e29`. The three new lease, prelatched
withdrawal and release-failure tests passed in 1.415s, zero failures/skips
(`Rig/temp/cross-store-review-325-native-boundary.jsonl`), with frozen HEAD and
clean status verified before/after. Source review confirmed effects admission
before the withdrawal-only gateway lock, sticky fail-stop, foreign barrier
preservation, no SQL serving authority and final protected ownership checks.
This is a bounded emergency-path acceptance; the complete typed Docker adapter,
serving driver activation and end-to-end hosted runtime gate remain incomplete.

## Reviewed concrete restart driver candidate

Worker commit `14c2913ad7709ff4e411a71c3424822455113700` adds the concrete current
serving restore driver, its command-executor tests and a six-line final
authorization/latch check in the existing stopped-container restart helper.
The latter closes a reproduced late-inventory race where authority could change
after the opening guard and before container start. No default factory is enabled.

Owner overlay checks against frozen integration dependencies passed four tests
and eight subcases in 186.077s; ordinary restart preservation passed one test and
two subcases in 46.707s. Independent review used only the candidate files over
exact integrated `7d60803`, verified both source/base identities and clean state,
and passed two tests plus five subcases in 94.448s, zero failures/skips
(`Rig/temp/cross-store-review-14c-restore.jsonl`). These cover completed-batch
restart, lost acknowledgment, before-start refusal, post-start drift and the
late-inventory no-effect regression. Overlay vet passed. Actual Docker remains
unverified. The first independent attempt failed before tests because one Git
identity lookup omitted its checkout directory; the corrected run is the recorded
application evidence.

The user explicitly approved local integration of `14c2913`; it was integrated
as `37cd6787087929c58aaa947a5bbbca93c58af677`. Its three-file delta is based on
already integrated `d5ec8b9`. The parent integration check passed three tests and
two subcases in 75.756s, zero failures/skips
(`Rig/temp/m3-current-serving-driver-integration.jsonl`):

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(ManagedGatewayCurrentServingRestoreRuntimeRechecksAuthorityAfterFinalStoppedInventory|GatewayCurrentServingRestoreKeepsCompletedBatchAndTerminalSQL|GatewayCurrentNativeEmergencyCrossesExistingFailStopOnlyToWithdraw)$'
```

Vet and the full Go build passed. This approval authorizes local integration only.

## Typed SQL and concrete restart composition

The combined real SQLite/protected typed commit, ordinary redeploy, startup
authorization and concrete command-executor test initially refused both stable
and completed-batch restoration before physical effects (70.831s;
`Rig/temp/m3-current-serving-sql-executor.jsonl`). Focused diagnosis proved the
network ownership checks valid, then identified a create-time configuration
fingerprint mismatch (26.299s;
`Rig/temp/m3-current-serving-sql-executor-final-base.jsonl`). The verifier had
unconditionally used the legacy digest envelope `{version,args,networks}` while
the retained typed constructor uses `{version,args}`.

The verifier now selects exactly the envelope required by the validated terminal
format. It does not try alternate formats or rewrite retained history. The new
test covers both stable restart and completed SQL disable recovery followed by
marker retirement and an idempotent stable restart. It checks lost start
acknowledgments, unchanged SQL, preserved immutable files and retained recovery
state until retirement. Docker commands and reachability are simulated.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^Test(GatewayCurrentServingRestoreComposesSQLWithConcreteExecutor|ManagedGatewayCurrentPhysicalRuntimeBindsImmutableContainerAndDynamicNetworks)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

Both tests and all six subcases passed in 127.179s, zero failures/skips
(`Rig/temp/m3-current-serving-sql-executor-green.jsonl`), including existing legacy
container identity, label and publication-boundary preservation. Vet, full build,
gofmt and `git diff --check` passed. Independent review remains pending. No default
factory activation, publication, deployment or hosted Docker acceptance is claimed.

Independent source/security review accepted `59f68db`: the terminal union is
validated before selecting its exact digest envelope; neither a fallback digest
nor a rewritten receipt is accepted. Independent execution is recorded separately
when available.

## Emergency withdrawal with retained no-effect abort and corrupt native routes

Combining the previously separate retained-abort and corrupt-native-route cases
exposed a refusal: both source-route and native-v2 corruption caused incomplete
ownership enumeration before shutdown (one test/two subcases, 34.638s;
`Rig/temp/m3-native-emergency-retained-abort-corruption-red.jsonl`).

The new withdrawal-only fallback applies only after the ordinary history scanner
fails. It requires exactly the committed generation-zero native journal and
state-file path, no native retirement/abort or additional native generation, and
a contiguous sequence consisting entirely of typed no-intent/no-effect aborts.
Every strict checkpoint must bind its frozen native state to the same journal,
source digest and full lineage; every terminal must match that exact checkpoint.
Operations must be unique, current-route bundles are forbidden, and both complete
safe-file censuses are confirmed again before returning. It returns only a
no-current/no-unresolved ownership result, never serving authority. The ordinary
startup scanner remains unchanged and continues rejecting corrupt routes.

Both corruption cases passed in 35.134s, zero failures/skips
(`Rig/temp/m3-native-emergency-retained-abort-corruption-green.jsonl`). The expanded
suite passed four tests/seven subcases in 93.062s, zero failures/skips:

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayCurrentNative(AbortEmergencyRequiresCompleteBoundHistory|Emergency(FallbackRetainsAbortedRebindHistory|RetainedAbortSurvivesCorruptNativeRoute|RefusesTypedCommittedOwnership))$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=2m ./internal/generatedingress -run '^TestGatewayCurrentNativeAbortEmergencyRequiresCompleteBoundHistory$/^orphan_current_bundle$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The first command's log is
`Rig/temp/m3-native-emergency-immutable-boundaries-green.jsonl`. It includes
positive-first refusal of missing checkpoints/terminals, a structurally valid
changed journal, a structurally valid changed checkpoint that disagrees with its
terminal, an extra native generation, and post-stop unknown history. The added
valid orphan-current-bundle subcase passed in 33.872s (one test/one subcase;
`Rig/temp/m3-native-emergency-immutable-orphan-green.jsonl`). An initial test-only
compile error used a nonexistent route-generation field; it was corrected to a
valid endpoint-ID change before the successful boundary run. Vet and full build
passed; gofmt and `git diff --check` passed.

Review has accepted the bounded design and source; independent execution remains
pending. Physical stop is injected, not live Docker. Corrupt native routes with
committed rebind ownership, mixed legacy history or earlier native generations
still return incomplete ownership and retain the startup latch. Those require
separate immutable-history support. A corrupt current route bundle additionally
requires a terminal-only exact-container withdrawal capability; no valid route
state is fabricated from incomplete evidence. M3 and the production adapter remain
incomplete.

Independent review accepted frozen `769354f3d783ebbc3c18ad0a311f2b82c04c04e8`
including `59f68db`. Two tests/three selected subcases passed in 73.075s, zero
failures/skips: real-SQL/concrete stable restoration (40.40s), retained-abort
source corruption (15.96s), and native-v2 corruption (16.46s). Evidence:
`Rig/temp/cross-store-review-769-59-composition-emergency.jsonl`. The reviewer
verified exact HEAD and clean status before/after and reviewed all six boundary
mutations plus late-history refusal. This is bounded acceptance of the recorded
scope; the limitations above remain open, and default activation remains closed.

## Immutable typed history for committed-current emergency withdrawal

The next positive-first test reproduced the remaining committed-current refusal
with corrupt source and native-v2 routes (one test/two subcases, 46.307s;
`Rig/temp/m3-typed-emergency-native-corruption-red.jsonl`). The new withdrawal-only
history reader replaces the narrower no-effect-abort fallback. It validates prior
native aborts/retirements with their existing historical validators, binds the
committed native journal to an exact frozen checkpoint, and walks every contiguous
typed checkpoint, intent, progress record and terminal with the existing format,
lineage, resource and predecessor-port validators. Duplicate operations, gaps,
foreign formats, missing terminal evidence and crossed bindings remain errors.
An active tail is retained as unresolved, never reported as all traffic absent.
Complete history and safe-file presence snapshots must remain unchanged.

Only owned-stop enumeration and its terminal revalidation use this reader. It
returns no source-route bytes and cannot supply serving authority. Both initial
target construction and before/after effect guards now use the same immutable
terminal selection. Normal startup selection and its SQL checks remain strict.

```powershell
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=3m ./internal/generatedingress -run '^TestGatewayCurrentTypedEmergencyWithdrawsWithCorruptNativeRoute$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayCurrentTypedEmergency(ValidatesPriorNativeGenerations|RetainsRepeatedCommitOwnership)$'
go test -mod=readonly -buildvcs=false -p=1 -json -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayCurrent(NativeAbortEmergencyRequiresCompleteBoundHistory|StartupEmergencyStop)'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The initial injected-driver integration passed two tests/four subcases in 80.390s
(`Rig/temp/m3-typed-emergency-native-corruption-green.jsonl`). The strengthened
test composes real SQLite/protected typed commit with the concrete managed stop
adapter and command-runner simulation: both native corruption cases passed in
48.752s, including lost stop acknowledgment, exact container ID, unchanged current
state/history and sticky admission latch
(`Rig/temp/m3-typed-emergency-concrete-stop.jsonl`). No SQL reader is available to
the withdrawal path. Actual Docker remains unverified.

Prior native retired/aborted generations, missing predecessor receipts, two real
SQL/protected typed commits, distinct retained ownership and a structurally valid
terminal with a crossed claim digest passed two tests/two subcases in 41.408s
(`Rig/temp/m3-typed-emergency-retained-generations.jsonl`). Vet and full Go build
passed. The preserved native-abort negative tests continue to cover missing
checkpoints/terminals, valid changed native journal/checkpoint, extra incomplete
native generation, orphan current bundle and late history drift.

Existing emergency/API preservation passed nine tests/six subcases in 130.203s,
zero failures/skips (`Rig/temp/m3-typed-emergency-owned-stop-preservation.jsonl`).
Gofmt and `git diff --check` passed. Independent source review found no serving
callsite or weakened normal selector; frozen executable review remains pending.

This supersedes the earlier generation-zero-only and committed-typed/native-route
corruption limitations. Mixed legacy rebind history under native corruption still
refuses withdrawal, and corrupt current-route bundles still require a distinct
terminal-only stop capability. No state is reconstructed from a hash. The active
database-committed restart consumer, full typed runtime adapter, default activation
and hosted gates remain incomplete.

Independent executable/source/security review accepted frozen
`22df0ae30ed73d43a8487bae32149b297ea385e3`. Two tests/three selected subcases passed
in 25.689s, zero failures/skips: concrete managed stop with lost acknowledgment
under native-source corruption (24.74s), and retained native retired/aborted
generations (0.36s/0.34s). Log:
`Rig/temp/cross-store-review-22df-typed-emergency.jsonl`. Exact HEAD and clean
status were verified before/after. The reviewer confirmed withdrawal-only
callsites, empty source routes, exact immutable bindings and complete final
snapshot checks. This acceptance retains the stated corrupt-current and mixed
legacy limitations.

## Active database-committed serving restoration (2026-10-06)

Adds a private, already-locked recovery consumer and a distinct managed physical
driver entry for the exact typed `database_committed` attempt. The caller holds
the effects lease and both gateway locks before taking its commit-barrier latch.
Both process admission latches must remain clear. The SQL fence remains active
throughout; this operation neither transitions SQL nor writes protected state.

Authorization pins the complete same-transaction `HostingGatewayStartupSnapshot`,
active claim/roster/runtime heads, exact database-commit event, typed commit
terminal and original baseline, every runtime component (including loopback
routes), LAN grants/disables and retained provenance. Immutable protected files,
exact selected current state and caller-supplied attempt authority are reread
around physical effects and after proof. Prepared/rollback-capable requests,
missing commit evidence, pending operations, recovery batches and changed
pre-entry baselines are refused. Ordinary `selectExact` and lower-level
`restartStopped` admission checks are unchanged; no generic fence exception or
fabricated no-active snapshot is introduced.

The runtime accepts exact serving state idempotently or restarts its exact owned
stopped container. Lost acknowledgments require fresh exact observation under
the same guards. Authority failure latches admission and stops only an unchanged
exact protected owner. If protected state changes after start, compensation
refuses to substitute that new state and reports `candidateMayBeLive`; the SQL
fence and process latch remain. This retains the separately tracked need for a
terminal-only emergency stop capability.

Executable evidence uses actual SQLite transitions and protected history, with
the concrete managed Docker command adapter and simulated Docker/probe boundary:

- Initial active-fence/lost-ack/idempotent/latch test passed 1 test in 43.752s:
  `Rig/temp/m3-active-committed-serving-first.jsonl`.
- Runtime authority before/after start, protected state before start, and attempt
  authority after start passed 1 test/4 subcases in 109.872s:
  `Rig/temp/m3-active-committed-serving-boundaries.jsonl`.
- Crossed SQL commit event before start and changed protected state after start
  passed 1 test/2 subcases in 57.279s:
  `Rig/temp/m3-active-committed-serving-extended.jsonl`.
- Shared ordinary restart proof, final stopped-inventory guard, full runtime
  component census and active-fence positive regression passed 4 tests/10
  subcases in 143.776s:
  `Rig/temp/m3-active-committed-serving-regression.jsonl`.
- Final baseline refinement reran the active-fence test in 42.696s, including
  exact typed baseline equality and refusal of a valid newer protected revision
  already present before entry:
  `Rig/temp/m3-active-committed-serving-exact-baseline.jsonl`.

All listed runs had zero failures/skips. Commands (with the existing workspace
Go cache):

```text
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^TestGatewayRebindCommittedServingRestoreKeepsActiveSQLFence$' -count=1 -json
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^TestGatewayRebindCommittedServingRestoreRefusesBoundaryDrift$' -count=1 -json
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^TestGatewayRebindCommittedServingRestoreRefusesBoundaryDrift$/(SQL_event_before_start|protected_after_start)$' -count=1 -json
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^(TestGatewayRebindCommittedServingRestoreKeepsActiveSQLFence|TestManagedGatewayCurrentServingRestoreRequiresFullAndExactAuthorityAtProof|TestManagedGatewayCurrentServingRestoreRuntimeRechecksAuthorityAfterFinalStoppedInventory|TestGatewayCurrentServingRuntimeCensusRejectsIncompleteOrCrossedComponents)$' -count=1 -json
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

Vet, full Go build, gofmt and diff whitespace checks passed. The predecessor
fixture gained an initial endpoint parameter so SQL and protected runtime IDs
agree before immutable journals are created; existing fixture defaults remain
unchanged. No committed fixture identity is rewritten.

This is an internal capability awaiting the coherent typed adapter and its
independent integration review. Default adapter activation, live Docker, Linux
race checks, hosted CI and full M3 acceptance remain outstanding. No branch was
published, merged into another branch or deployed for this unit.

Independent executable/source/security review accepted frozen
`c7fbd58ec43eb50e80582e23450c76590dfb70d0` for this bounded capability. Two tests/one
selected subcase passed in 74.955s, zero failures/skips. The positive case
(44.88s) covered lost acknowledgment, exact baseline refusal, idempotence and
both latches; post-start runtime-authority loss (29.84s) proved exact-owner
withdrawal. Log: `Rig/temp/cross-store-review-c7-active-serving.jsonl`.

```text
go test -mod=readonly -count=1 -json -timeout=4m -run '^TestGatewayRebindCommittedServingRestore(KeepsActiveSQLFence|RefusesBoundaryDrift)$/^(runtime_after_start)$' ./internal/generatedingress
```

The reviewer verified exact HEAD and clean status before/after and found no
must-fix source/security issue. This is not acceptance of default activation or
the full milestone. The separate initial-publication missing-Hosting-authority
guard is being repaired in the typed-runtime worker. Existing cross-store
contracts close SQL transactions before Docker and revalidate afterward;
revocation and component-failure reporting remain available. No additional SQL
mutation-fence migration was introduced to suppress those safety operations.

## Terminal-only emergency withdrawal for corrupt current routes (2026-10-06)

Adds a separate typed-terminal stop capability with no current route state,
current SQL selection, route reconstruction or serving proof. Its target binds
the exact validated typed intent/terminal, successor identity, network plan and
retained handover local port. A complete protected history scan establishes
ownership; duplicate final IDs across retained terminals are refused. The
emergency caller owns the effects lease and gateway locks and keeps its fail-stop
latch set. A foreign commit barrier is preserved.

Only this emergency presence reader may fingerprint current-route bytes without
decoding their JSON. Ordinary presence and serving readers remain strict. Both
the full immutable-history snapshot and complete protected presence fingerprints
are compared before/after effects, including corrupt current-file replacement.
If native and current routes are both corrupt, the previously validated typed
immutable-history reader supplies ownership without recreating either file.

The physical adapter exposes inspection and exact-ID stop only. It checks the
retained pinned image, volume identity/mountpoints, ingress identity, absence of
the stage container, complete operation/generation resource census, exact name
and ID agreement, full immutable container configuration, volume users, and all
configured LAN/loopback listener ports. Current application-network membership
is mutable and is not frozen to the historical create roster. Shared metadata
checks were extracted from the ordinary runtime into `gateway_final_ownership.go`;
ordinary target validation and serving/config checks remain in their callers.
Truncated resource inventories now fail closed.

Lost stop acknowledgments require a fresh exact inventory and listener absence.
A container already stopped or genuinely absent produces no effect. Corrupt or
missing current routes still produce an incomplete startup result after a
successful withdrawal, with verified/stopped counts retained. No SQL transition,
route repair, history rewrite, start, copy, network mutation or resource deletion
occurs. This path conservatively requires retained image/volume/network inventory
even when the exact container is absent; separately removed or ambiguous retained
resources remain unresolved. Mixed legacy rebind history under simultaneous
native/current corruption retains the previously documented refusal.

The first owner run exposed a reporting defect: strict current-bundle inspection
returned an empty census even though terminal-only withdrawal stopped the exact
container. The result now retains the independent terminal census. A negative
fixture also accidentally reused the real final ID (`9` repeated 64 times); it
now uses a distinct ID and resets each case's simulated inventory. This initial
run failed in 92.134s (`Rig/temp/m3-terminal-only-emergency-first.jsonl`) and is not
acceptance evidence.

Corrected executable evidence uses actual SQLite typed commits/protected history
and the concrete Docker command adapter with deterministic command/probe fixtures:

- Corrupt, missing, simultaneous native/current corruption; lost acknowledgment;
  stopped/absent idempotence; changed ID/image/labels/ports; extra owner; and late
  terminal/current-file change: 3 tests/10 subcases passed in 170.409s,
  `Rig/temp/m3-terminal-only-emergency-corrected.jsonl`.
- Truncated ownership inventory, changed volume mountpoint and a remaining
  listener: 1 test/3 subcases passed in 27.833s,
  `Rig/temp/m3-terminal-only-emergency-proof-boundaries.jsonl`.
- Shared immutable/dynamic-network metadata, ordinary exact-owned stop/absence,
  foreign barrier, native corruption withdrawal, and active database-committed
  restart: 5 tests/9 subcases passed in 168.437s,
  `Rig/temp/m3-terminal-only-emergency-regression.jsonl`.

All corrected runs had zero failures/skips. Exact commands:

```text
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^TestGatewayCurrentTerminalEmergency' -count=1 -json
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^TestGatewayCurrentTerminalEmergencyRequiresCompleteInventoryAndListenerAbsence$' -count=1 -json
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^(TestManagedGatewayCurrentPhysicalRuntimeBindsImmutableContainerAndDynamicNetworks|TestManagedGatewayCurrentPhysicalRuntimeExecutorStopsOnlyExactOwnedIdentity|TestGatewayCurrentStartupEmergencyStopNeverClearsForeignCommitBarrier|TestGatewayCurrentTypedEmergencyWithdrawsWithCorruptNativeRoute|TestGatewayRebindCommittedServingRestoreKeepsActiveSQLFence)$' -count=1 -json
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

Vet, full Go build, gofmt and diff whitespace checks passed. Actual Docker and Linux race behavior remain for hosted
CI. The typed runtime adapter, default activation and complete M3 acceptance are
still unfinished. No publication, cross-branch integration or deployment was
performed for this change.

Independent executable/source/security review accepted frozen
`f1e2c0787f012e231821c4460ae72033353dfa4a` for this bounded withdrawal capability.
Two tests/three subcases passed in 63.863s, zero failures/skips, with exact HEAD
and clean status verified before/after. The cases exercised simultaneous native
and current-route corruption, lost stop acknowledgment and idempotence, changed
volume identity refusal, and a listener that remains after stop. No must-fix
source/security findings were reported. Log:
`Rig/temp/cross-store-review-f1-terminal-withdrawal.jsonl`.

```text
go test -mod=readonly -count=1 -json -timeout=4m -run '^TestGatewayCurrentTerminalEmergency(StopsDespiteCorruptCurrentBundle|RequiresCompleteInventoryAndListenerAbsence)$/^(native_and_current_corrupt|changed_volume_identity|listener_remains)$' ./internal/generatedingress
```

## Current active rebind approval authority (2026-10-06)

The full Hosting snapshot now projects the current administrator roles of both
the active rebind approver and successor-profile configuration approver in its
existing SQLite read transaction. These may be different users from each other
and from the LAN/native gateway approvers. The projection binds the exact active
operation, stored spec version, and both immutable approval actor IDs. Strict
legacy/V2 union validation refuses missing, crossed or unsupported claim forms;
an absent active claim requires an absent projection at the serving boundary.

Role revocation is observable as false authority flags. It does not make the
immutable recovery history unreadable or prevent ownership-based withdrawal.
There is no new SQL mutation fence, migration, persisted approval rewrite or
role-change restriction. The active database-committed restart checks the flags
before selection and again on every fresh authority read, in addition to its
existing full snapshot digest, protected history and runtime census checks.
Normal serving restoration also rejects a stale active-approval projection.

Tests use three distinct administrators for the LAN/native profile, rebind,
and configure approvals. Real SQLite demotion of either active approver before
restart prevents any start command. Demotion after the concrete Docker command
adapter starts the exact owned container is detected by the following guard;
the exact container is then stopped while the SQL fence and immutable history
remain intact. The adapter tests use deterministic Docker command/probe fixtures,
not a live Docker daemon. These boundary checks do not constitute continuous
permission monitoring after startup returns.

Owner verification:

- Focused same-transaction projection, revocation/ownership and crossed-binding
  cases: 3 tests/12 subcases passed in 1.964s. Log
  `Rig/temp/m3-active-rebind-approvals-sql.jsonl`.
- Entire appaccess package: 126 tests/175 subcases passed in 98.669s. Log
  `Rig/temp/m3-active-rebind-approvals-appaccess.jsonl`.
- Concrete active committed-serving adapter: 1 test/5 subcases passed in
  134.901s. Log `Rig/temp/m3-active-rebind-approvals-runtime.jsonl`.
- Existing ordinary startup, completed recovery batch and active SQL-fenced
  restart regressions: 3 tests passed in 107.333s. Log
  `Rig/temp/m3-active-rebind-approvals-regression.jsonl`.

All runs above had zero failures/skips. Exact commands:

```text
go test -mod=readonly -buildvcs=false -p=1 ./internal/appaccess -run '^TestHostingGatewayActiveRebindApprovals' -count=1 -json
go test -mod=readonly -buildvcs=false -p=1 ./internal/appaccess -count=1 -json
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^TestGatewayRebindCommittedServingRestoreRequiresCurrentApprovers$' -count=1 -json
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^(TestGatewayCurrentServingRestoreUsesRealCompleteSQLAuthority|TestGatewayCurrentServingRestoreKeepsCompletedBatchAndTerminalSQL|TestGatewayRebindCommittedServingRestoreKeepsActiveSQLFence)$' -count=1 -json
go vet -mod=readonly ./internal/appaccess ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

Vet, full Go build, gofmt and diff whitespace checks passed. Independent frozen review, actual Docker/Linux
race checks, full typed-adapter integration and M3 acceptance remain outstanding.
No publication, cross-branch integration or deployment was performed for this unit.

## Approved integration into the typed runtime branch (2026-10-07)

The user explicitly authorized local integration of history through
`f4b2e63e16df6a52d1279697080e0c887e4f0c0c` into
`feature/hosting-m3-rebind-cross-store-runtime`, whose previous head was
`378021b6bf840d9d0551ce10372d1d802907bf9a`. The merge retains both parents.
Later commits from the dependency branch are outside this integration.

The actual merge required one manual conflict resolution, in the predecessor
fixture. Its endpoint and distinct LAN-approver parameters are now initialized
together before immutable journals are created. The current physical runtime
driver and executor test merged automatically and exactly match incoming f4.
The original untracked handover QA file remains unmodified and unstaged:
`internal/generatedingress/gateway_rebind_typed_handover_runtime_test.go`, SHA256
`D4F619FC3A2EA6D3551FE308AF21F8CA6304B874D461E4F12B28D43CE6843140`.
An identical local backup is retained at
`Rig/temp/m3-f4-integration-20261007-original-qa-D4F619.go`.

### Integration defect and correction

The initial combined tree `f9e659eebae58a30994d537d408ec404520c86eb`
compiled, but its seven-test runtime selection failed the distinct LAN-approver
demotion case: one physical action occurred where zero were required. Six other
top-level tests and twelve subcases passed; one top-level test and one subcase
failed, with zero skips, in 262.078s. This failed run is retained, not counted as
acceptance. Log: `Rig/temp/m3-f4-integration-20261007-runtime.jsonl`, SHA256
`DACD4E8CECA8E6432D89E885826556EA5145A87322EDAA716D9299531FF820E8`.

Incoming recovery history deliberately remains readable after role revocation.
The typed forward path had relied on the recovery reader to reject revoked
permissions. Its new forward boundary brackets the existing ownership and
protected-history check with two equal full Hosting snapshots. It requires
current active rebind/configure approvals, exact raw roster grant requests
(including the protected approver identity), current LAN administrator roles,
and matching rebind/runtime-head projections. Missing, crossed, revoked or
changing authority refuses the forward effect. The ownership reader and rollback
guard remain separate so revoked serving permission does not prevent withdrawal.

This is a compatibility correction for the approved dependency integration. It
does not fix the separately reproduced rollback predecessor-serving defect or
complete forward-only withdrawal and terminal/current attestation integration.
Production typed factories remain closed.

### Executable evidence

The corrected source tree is `e9aee7314727e63801f8e3919f494bd52bebaa02`;
the following evidence documentation is the only subsequent change.
All successful runs below had zero failures/skips. Commands ran from the runtime
checkout with `GOCACHE=Rig/.go-cache-m3`.

| Check | Result | Local log suffix / SHA256 |
| --- | --- | --- |
| Initial appaccess approval projections | 3 tests, 12 subcases, 2.163s | `appaccess.jsonl` / `E8B30D216B76A9961A9B6E71C955D1B934D2E7D00B9B473B52F6FDF70BE51817` |
| Initial hostd emergency dispatch | 3 tests, 14 subcases, 0.725s | `hostd.jsonl` / `F64A096831083429DD48692CB54073A054455CA412B224B92ACA1508B93AD375` |
| Corrected authorization, rollback compatibility, active committed restart | 3 tests, 12 subcases, 100.127s | `authority-regression.jsonl` / `1D7AF1E0A8EEF211A5EEE76929DE4A91214367D82A5F7511CCF8CB5173BCD743` |
| Complete authorized stage prefix | 1 test, 32.454s | `forward-prefix.jsonl` / `0984DF92A26263C1FCE1C7CCCFCB4EBEF52295D7EF57B35ACC12D5D2B8C4CE4A` |

Log paths above share prefix `Rig/temp/m3-f4-integration-20261007-`.
The corrected authorization test includes ten cases: authorized distinct actors;
LAN, rebind and configure demotion before effects and between snapshot reads;
and missing active approval, missing grant and crossed grant projections.
Each SQL demotion verifies exactly one affected user. Refusal leaves the protected
prefix intact and recovery history readable. The rollback regression verifies
durable intent before simulated cleanup. These use real SQLite/protected state
with simulated Docker effects; they do not prove live Docker withdrawal.

```text
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^$' -count=1
go test -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress ./cmd/hostd ./internal/appaccess -run '^$' -count=1
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=3m -json ./internal/appaccess -run '^TestHostingGatewayActiveRebindApprovals'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=3m -json ./cmd/hostd -run '^TestGatewayStartupEmergency'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=12m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedDriverRechecksDistinctLANApproverBeforePhysicalEffect|TestGatewayRebindCommittedServingRestoreRequiresCurrentApprovers|TestGatewayRebindCommittedServingRestoreKeepsActiveSQLFence|TestManagedGatewayCurrentPhysicalRuntimeExecutorRestartsOnlyAuthorizedStoppedState|TestManagedGatewayCurrentPhysicalRuntimeBindsImmutableContainerAndDynamicNetworks|TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists|TestInspectGatewayRebindPredecessorAcceptsExactReadOnlyPair)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=8m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedDriverRechecksDistinctLANApproverBeforePhysicalEffect|TestGatewayRebindTypedDriverPersistsRollbackIntentBeforeEffects|TestGatewayRebindCommittedServingRestoreKeepsActiveSQLFence)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=5m -json ./internal/generatedingress -run '^TestGatewayRebindTypedStageDriverPersistsCompleteStagePrefix$'
go vet -mod=readonly ./internal/appaccess ./internal/generatedingress ./cmd/hostd
go build -mod=readonly -buildvcs=false ./...
```

The first two commands are compile-only: baseline 1.251s, then combined packages
1.258s/1.637s/0.848s; neither executes behavioral tests. The seven-test command
is the initial failed run described above. Vet, full Go build, gofmt and staged
whitespace checks pass after the correction. Independent orchestration and
security source reviews accepted the bounded correction, preserving withdrawal
authority and the closed production factories.

No full repository suite, frontend build, live Docker, Linux race or complete
typed-adapter acceptance is claimed. Existing broader adapter defects remain
open. No PR, push, GitHub merge or deployment is authorized by this local
integration approval; none was performed. The next runtime work is serving
authority during rollback and committed recovery, followed by hosted acceptance
after separate publication approval.

## Typed rollback serving authority (2026-10-07)

Purpose: retain ownership-based successor cleanup while requiring current serving
authority before accepting or restarting the predecessor during typed rollback.
This local unit follows approved integration commit
`b681cf9255a4f32d53d248be32841f895095920e`; it imports no further branch history.
The reviewed implementation/test tree is
`5fffe5c8d82320334656e57203144619644b775b`, before this evidence update.

### Invariants and implementation

- Exact retained ownership authorizes cleanup and withdrawal even when an
  approver loses their role. Serving requires equal complete Hosting snapshots,
  the active rebind/configure approvals, exact current LAN grant approvals,
  runtime heads and component census, and the selected protected predecessor.
- Predecessor authority is checked before and after restart, before and after
  appending physical rollback completion, on cached completion replay, before
  terminal receipt installation, and immediately before SQL release. Failed or
  uncertain appends reread the actual protected prefix/receipt.
- Refusal stops only the exact immutable predecessor container when ownership
  is proven, checks its stopped state after listener probes, and preserves the
  SQL fence. Ambiguous listeners, ownership, readback, or retained successor
  containers report potentially-live state. Rollback's diagnostic precedes the
  earlier forward cause in the joined error so `errors.As` sees that status.
- Completion records and receipts already written remain immutable after a
  later denial. No terminal SQL transition is accepted on failed confirmation.
- Successful rollback proves two equal absent successor inventories and network
  topology around a freshly authorized serving predecessor observation.

The shared component census is reused for native and rebound predecessors.
Two prerequisite runtime corrections restrict config-phase matching to legal
effect phases and preserve the pinned image identity when checking that mutable
successor resources were removed. Production typed factories remain closed.

The previously untracked handover QA file is deliberately promoted in this unit.
Its simulator now restores original application-network addresses and ingress
membership on predecessor restart; admission fixtures establish distinct actors
and matching SQL endpoints before creating immutable records. The ambiguous
probe test uses the concrete coordinator and durable final-container prefix.
The original backup from the preceding integration still has SHA256
`D4F619FC3A2EA6D3551FE308AF21F8CA6304B874D461E4F12B28D43CE6843140` at
`Rig/temp/m3-f4-integration-20261007-original-qa-D4F619.go`.

### Failures retained as evidence

All logs below have prefix `Rig/temp/m3-rollback-authority-20261007-`.

| Log suffix | Actual outcome | SHA256 |
| --- | --- | --- |
| `baseline.jsonl` | 1 failing test, 75.323s: demoted rebind approver still allowed predecessor serving and completion after cleanup | `425E7FB987C742AEFCCA942C49D0DDF4D4FAEAB9EFFF8D2E76756C61451104D1` |
| `matrix.jsonl` | 5 subcases passed; 3 subcases and parent failed, 626.980s | `BC82091316E8D244CA4D1004D9C6BD9D1DC7B61B367A0D26BB7D7C44369F208D` |
| `regressions.jsonl` | 9 tests/28 subcases passed; 1 test failed, 714.021s | `4FFDED14B34AF1180411B6EAF2B9490295BF334D90D164626D0B9B9BB86F0A66` |
| `final-refusal.jsonl` | Readback test/2 subcases passed; retained-successor setup failed, 148.620s | `EC653C19E7C641F1548A12B59FBAEA44DC437CEE8C85452EADBC8431F6B95A8A` |
| `ambiguous-probes.jsonl` | Corrected probe test passed; run intentionally stopped at 107.142s because its optional subtest regex selected the whole matrix | `4AB1BAC533C74CD1D3B70910F83D6A471C5BB36E2FEAA4F495C045A2DAA9E980` |

The matrix's three failures were two restart fixtures that stopped the
predecessor before reconciliation could reach the intended boundary, and a real
diagnostic-ordering defect masking potentially-live rollback state. The corrected
restart fixtures stop after exact successor-network removal and assert zero
starts for pre-start revocation or exactly one start for post-start revocation.
The regression failure used a direct final-create fixture without the durable
phase required to reach the ambiguous listener probe; it now runs through the
coordinator and asserts history ends at sequence 14 without cutover or starts.
The retained-successor test now enters through the concrete forward handover,
then injects exact stage-removal failure and checks rollback intent at sequence
14. Its original direct-prefix-12 early refusal was not localized and is not
counted as evidence of successful cleanup or a production fix.

The five successful original matrix cases cover distinct rebind/configure/LAN
approvers, completion-write refusal, and lost completion acknowledgment followed
by cached replay. The successful regression cases cover the complete forward
sequence, create-boundary drift, durable config phases, stage-gap rollback,
final-removal replay, the component census, rollback intent before effects, and
four commit/recovery receipt and SQL-release refusals. These earlier mixed runs
are recorded as partial/intermediate evidence, not green package runs.

### Corrected focused verification

Commands ran from the runtime checkout with `GOCACHE=Rig/.go-cache-m3`. Tests use
real SQLite and protected records with simulated Docker commands/probes.

| Log suffix | Result | SHA256 |
| --- | --- | --- |
| `restart-boundaries.jsonl` | 1 test/2 subcases passed, 144.822s | `092B33BB04B4CAFAC955927EFC023DFAB73F70DA1D7BE7AEA905504A9D40208E` |
| `retained-successor.jsonl` | 1 test passed, 58.640s | `34F5FB34EBFDD8847A549C274318AD377BBF18646BDB61F9CD94C93FC71B3F5C` |
| `ambiguous-diagnostic.jsonl` | 1 test/1 subcase passed, 67.023s | `2E4904416E2D6878B7272228DDA89FAC6CE73AC0D3B6754F96534247B6DFF7D4` |
| `final-boundaries.jsonl` | 3 tests/2 subcases passed, 155.096s | `F844AFE2188A041A48DDC0BB3FDF3E022A4F2CBD72C93476D2BB534BC4C8D143` |

The restart run precedes only the subsequent diagnostic-ordering change and
test-fixture corrections. Retained-successor, ambiguous-diagnostic and
final-boundaries runs use the final implementation. The final boundary run
checks durable cutover probe refusal, stopped-during-serving and
restarted-during-withdrawal readbacks, and the closed production factory.
Every completed focused run above has zero
failures/skips and verifies that the selected tests were discovered.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=18m -json ./internal/generatedingress -run '^TestGatewayRebindTypedRollbackWithdrawsAfterAuthorityLoss$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=18m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedHandoverRuntime|TestGatewayRebindRollbackConfirmationFencesCommitAndRecovery|TestGatewayRebindTypedDriverPersistsRollbackIntentBeforeEffects|TestGatewayCurrentServingRuntimeCensusRejectsIncompleteOrCrossedComponents)'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=6m -json ./internal/generatedingress -run '^TestGatewayRebindTypedRollbackWithdrawsAfterAuthorityLoss$/(before_predecessor_start|after_predecessor_start)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=4m -json ./internal/generatedingress -run '^TestGatewayRebindTypedRollbackReportsRetainedSuccessor$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=3m -json ./internal/generatedingress -run '^TestGatewayRebindTypedRollbackWithdrawsAfterAuthorityLoss$/^listener_absence_remains_ambiguous$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=5m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedRollbackRequiresFinalPhysicalReadbacks|TestGatewayRebindTypedHandoverRuntimeRejectsAmbiguousWithdrawalProbe|TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd ./internal/appaccess
go build -mod=readonly -buildvcs=false ./...
```

The first two commands are the mixed runs described above. Vet and the full Go
build pass after the final production change. Formatting and whitespace checks
pass. Independent orchestration/security source review accepted this bounded
change; broader M3 and production acceptance remain open.

### Limits and remaining work

A fresh local `docker info --format '{{.OSType}} {{.ServerVersion}}'` failed
because `npipe:////./pipe/dockerDesktopLinuxEngine` was unavailable. No live
Docker, Linux race, frontend build, or full repository test-suite pass is claimed.
Serving checks occur at coordinator/runtime boundaries; this does not claim
transaction-atomic role revocation against concurrent SQL updates.

Forward-only withdrawal after lost authority, active DB-committed recovery versus
terminal current attestation, complete typed-adapter integration, and hosted
Docker/process acceptance remain required before opening production factories
or declaring M3 complete. Disablement remains the closed factory gate; rollback
of this local code change must preserve all immutable protected history.
No push, PR creation, GitHub merge or deployment was performed for this unit.

## Receipt-bound forward serving authority (2026-10-07)

This local unit continues from `ae0176031a251777bd657e4977f8706ff310bffe`
on `feature/hosting-m3-rebind-cross-store-runtime`. The approved dependency
integration remains `b681cf9255a4f32d53d248be32841f895095920e`; the guarded
restart driver `14c2913` is an ancestor. No additional branch history was
imported. The reviewed, staged source/test tree before this documentation is
`a1781a8c8861fb06b4c32bf11b3d856d94bf52bf` (eight Go files).

### Purpose and invariants

A protected COMMIT receipt makes recovery forward-only even before the SQL
database-committed transition. Cached physical completion now requires fresh
serving authority and physical/configuration/publication proof. Commit and
recovery confirm that proof before each subsequent SQL transition.

Receipt-bound failures attempt ownership-only withdrawal under the held
coordinator locks. Cleanup uses an independent bounded context, refreshes actual
SQL phase after uncertain acknowledgments, and pins the original claim, receipt,
progress and immutable final container ID. It stops only that successor and
proves stopped readback plus absence of successor, predecessor and loopback
listeners. Lost stop acknowledgments require exact readback. Ambiguous ownership
or cleanup retains the fail-stop latch and reports that the candidate may be live.

Withdrawal neither starts the predecessor nor removes resources, rewrites
configuration/history, or reverses SQL. Current serving roles authorize continued
serving; ownership authorizes cleanup after those roles are revoked. Failure
defers cover post-receipt early returns and final SQL refusal even after the
commit barrier was set. Successful commits request zero withdrawals.

### Executable evidence

Commands ran from the runtime checkout with `GOCACHE=Rig/.go-cache-m3`.
Logs have prefix `Rig/temp/m3-forward-authority-20261007-`.

| Log suffix | Actual outcome | SHA256 |
| --- | --- | --- |
| `baseline.jsonl` | Expected failure on prior implementation: 1 test failed, 64.069s; revoked rebind approver left the successor running with no withdrawal | `AC8F204C5ADC6F865A79E0314916424AFC30B50EDC5122EA72352450F0921DFD` |
| `first-fix.jsonl` | Intermediate fix: 1 test passed, 75.185s | `979B8E4F801DB436C729CF1410D8C1E871916C33D9E39E73353F77C2C919CF79` |
| `matrix.jsonl` | Intermediate coordinator implementation: 1 test/12 subcases passed, 163.687s | `CF1C3C7E50375037255482017E67C71D43E4DEF5CC599100F578138BB254A6AB` |
| `coordinator.jsonl` | Before final failure defers: 3 tests/6 subcases passed, 187.410s | `07259A04BD6F8FAC7A06C36486F340618A51453D3360C8DF3F2394B87F865045` |
| `final-sql-failure.jsonl` | Final source tree: 1 test/1 subcase passed, 84.122s | `7CD2C79BEC6D316563C8207CB06DABF154A55F99643CA4D4DC2BBCF0B7F407FF` |
| `final-coordinator.jsonl` | Final source tree: 2 tests/6 subcases passed, 168.841s | `4FA9289218EE4DB530C07B2C318EBA6DCEE619F47533AD554C1A6C0632C7C1E3` |

Every green run terminated with exit zero, discovered its expected tests and
reported zero failures/skips. The baseline terminated with exit one. The matrix
covers distinct rebind/configure/LAN approvers, revocation during probes, stale
SQL phase after acknowledgment, stopped cached completion, lost stop acknowledgment,
ambiguous listeners, replacement IDs, caller cancellation, and restart during
absence probes. It asserts retained resources, unchanged SQL/history, active
fence and no predecessor start. Its runtime stop/readback logic is unchanged in
the final source; cleanup ownership subsequently moved to coordinator failure
defers. The final 13-case matrix was not rerun as one command.

The final SQL case uses a real completed typed handover, protected COMMIT receipt,
SQLite transitions through database-committed, and simulated Docker. It revokes
the rebind approver and refuses the final SQL write after the barrier is set,
then proves exactly one owned stop with SQL, history, fence and resources retained.
Only committed-current attestation during the active database-committed phase
is bounded in that fixture; forward confirmation and withdrawal use the concrete
typed runtime. The six final
coordinator refusal cases use real SQLite and a bounded physical driver, asserting
one withdrawal per refusal at prepared, successor-ready and database-committed
for both commit and recovery. The successful coordinator case asserts zero.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=4m -json ./internal/generatedingress -run '^TestGatewayRebindTypedForwardOnlyWithdrawsRevokedSuccessor$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=5m -json ./internal/generatedingress -run '^TestGatewayRebindTypedForwardOnlyWithdrawsRevokedSuccessor$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=8m -json ./internal/generatedingress -run '^TestGatewayRebindTypedForwardOnlyWithdrawsRevokedSuccessor$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=7m -json ./internal/generatedingress -run '^(TestGatewayRebindForwardConfirmationFencesCommitAndRecovery|TestGatewayRebindCoordinatorCommitsRealSQLAndProtectedBaseline|TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=4m -json ./internal/generatedingress -run '^TestGatewayRebindTypedForwardOnlyWithdrawsRevokedSuccessor$/^database_committed$/^final_SQL_failure$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=7m -json ./internal/generatedingress -run '^(TestGatewayRebindForwardConfirmationFencesCommitAndRecovery|TestGatewayRebindCoordinatorCommitsRealSQLAndProtectedBaseline)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd ./internal/appaccess
go build -mod=readonly -buildvcs=false ./...
```

The commands correspond to the log table in order. Vet, the full Go build,
read-only formatting and staged whitespace checks pass on the final source.
A test-fixture type-name compilation error was corrected before the final SQL
run; it is not counted as an executed behavioral test. Independent orchestration
and security source review accepted the frozen tree for this bounded scope.

### Limits and next work

The production typed factories remain closed. This evidence does not establish
complete M3 or production readiness. The next required adapter work includes:

- Pre-receipt sequence-17 failure: fresh confirmation can fail after physical
  completion while the request remains undecided. Receipt-bound withdrawal
  cannot authorize that case, and the existing rollback cannot consume that
  completed prefix. This remains a separate unresolved failure path.
- Authorized restoration of stopped serving during an active database-committed
  attempt, plus integration with terminal current-state attestation.
- Terminal-current failures after SQL releases the active fence; this unit's
  active-attempt withdrawal does not cover those or promise a retained fence.
- Transaction-atomic role revocation is not established by boundary checks.
- Actual Docker/process and hosted CI acceptance remain pending. The same-day
  Docker probe failed because the Docker Desktop Linux engine pipe was absent.
  No Linux race, frontend build or full repository test-suite pass is claimed.

Disablement remains the closed production factory. Any code rollback must retain
immutable protected history and active recovery state. No database provisioning,
remote publication, GitHub merge or deployment is part of this local unit.

## Completed physical handover before receipt publication (2026-10-07)

This unit continues locally from `2da28b6138f2ff21d5f5d4887f6bb5c5c8ccc74d`.
It closes the preceding section's pre-receipt sequence-17 withdrawal gap.
The source/test tree reviewed initially was
`94cf43fa9cc23210beeb33fb4f968e1c68b9cc1d`; the final source/test tree is
`66430c45ae3f34f0cdc7960516df99ef11f6a1e3`. The latter changes only the new
test file, correcting simulator progress and adding the unwritten-completion
boundary case. No branch history is imported.

### Behavior and failure boundaries

Before an undecided attempt falls back to rollback, a bounded independent
context re-reads exact ownership, SQL selection, protected progress and terminal
absence. A valid prefix without physical handover completion retains the existing
rollback path, including replay of rollback records numbered 17 or later. Durable
physical completion permits only exact successor withdrawal. Unprovable ownership
returns a potentially-live diagnostic and latches the process; it never implies
permission to roll back.

Withdrawal retains the active SQL fence, resources and immutable history. It
does not invent a receipt, advance SQL, restart the predecessor, or remove the
successor. The existing physical primitive proves exact stopped readbacks and
listener absence. A canceled caller does not cancel the bounded cleanup context.

The commit failure defer is armed as soon as a physical commit result is returned,
before validating that result or building its receipt. Recovery installation
failure also invokes cleanup when no receipt exists. If an intended receipt was
not installed, cleanup requires the actual undecided/prepared/no-database-commit
boundary and a physical result matching that intended receipt. An exactly retained
receipt uses its receipt-bound path. Conflicting or unreadable receipts remain
uncertain; continuing ownership guards enforce the proven history throughout.

### Executable evidence

Commands ran in the runtime checkout with `GOCACHE=Rig/.go-cache-m3`. Logs use
prefix `Rig/temp/m3-unreceipted-authority-20261007-`.

| Log suffix | Actual outcome | SHA256 |
| --- | --- | --- |
| `baseline.jsonl` | Expected failure on `2da28b6`: 1 test failed, 63.497s; role revoked after durable17, successor still running, no cleanup effects | `71ACFDA2E2052F86659B35A4799337A02534B79BD7D855A309C1FE353EFDC062` |
| `first-fix.jsonl` | Initial one-case regression passed, 73.315s | `827F139662D3E4CB610D6BAA02511A701592D5EFDE2AF5D58687CA4754D1E0AD` |
| `matrix.jsonl` | Mixed run: 2 tests/9 subcases passed; startup receipt fixture panicked; package failed, 298.819s | `19A4A7F02338DFC90069C6A46B4A9AA3D02532ED32F33C736D97349BFF71931B` |
| `regressions.jsonl` | 5 tests/15 subcases passed, 365.806s; zero failures/skips | `1E6AF0202D3C10AF25B0FC2EB92264153AC143ED42F3D25D62FCBE6B0DE5C428` |
| `final-boundaries.jsonl` | Final test tree: 3 tests/2 subcases passed, 288.128s; zero failures/skips | `6BE2A203D596C34047C4DF1F6E3A9AC77A58F7B3AA1CC57E73F1070A11BE7E7C` |

The mixed run's completed tests prove revocation after durable completion,
cancellation with a lost completion acknowledgment, authorized cached completion,
revoked/stopped replay, intended-but-absent receipt, exact retained receipt, and
conflicting receipt. Each asserts only the permitted exact stop (or no effect),
unchanged SQL/history, retained resources/fence and no predecessor start.

Its failure was in the existing simulated Docker runner's `createFinal`: the
new coordinator fixture passed the coordinator's appender, which did not update
the simulator's `effect` field as the existing fixture appender does. The fix
updates only simulator state after the real append succeeds. The first absent-
receipt subcase and its parent failed; panic prevented the durable-receipt and
pre-construction cases from running. This is retained as partial evidence, not a
green suite or a production failure. The corrected boundaries are run separately.

The final run proves both receipt installation failures through the actual startup
coordinator: a canceled caller with no receipt, and an exactly retained receipt
followed by cancellation/lost acknowledgment. Each performs one owned successor
stop, leaves the predecessor stopped, and preserves exact SQL, history and
resources. The pre-construction test verifies the coordinator invokes withdrawal
before refusing a mismatched returned physical proof. The unwritten-completion
test reaches durable sequence16 with the successor serving, refuses the sequence17
write, then proves rollback intent17/completion18, removed successor resources,
exactly one predecessor start and the retained SQL fence. Completed records are
never reinterpreted as this unwritten case.

The regression run covers the previous 13 receipt-bound cases, rollback after
final removal, two rollback-intent persistence cases, successful commit, and the
closed production factory. Production code is unchanged between these completed
runs and the final test tree. All tests use local SQLite/protected records and
simulated Docker; the commit pre-construction fixture bounds the physical driver.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=4m -json ./internal/generatedingress -run '^TestGatewayRebindTypedCompletedWithoutReceiptWithdrawsSuccessor$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=12m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedCompletedWithoutReceiptWithdrawsSuccessor|TestGatewayRebindTypedCompletedWithoutReceiptReplay|TestGatewayRebindTypedRecoveryWithdrawsFailedReceiptInstallation|TestGatewayRebindCommitWithdrawsBeforeReceiptConstruction)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=12m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedForwardOnlyWithdrawsRevokedSuccessor|TestGatewayRebindTypedHandoverRuntimeReplaysRollbackAfterFinalRemoval|TestGatewayRebindTypedDriverPersistsRollbackIntentBeforeEffects|TestGatewayRebindCoordinatorCommitsRealSQLAndProtectedBaseline|TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=8m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedRecoveryWithdrawsFailedReceiptInstallation|TestGatewayRebindCommitWithdrawsBeforeReceiptConstruction|TestGatewayRebindTypedUndurableCompletionStillRollsBack)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd ./internal/appaccess
go build -mod=readonly -buildvcs=false ./...
```

The first command ran both the red baseline and initial fix. Final-tree vet, full
Go build, read-only formatting and whitespace checks pass. Independent source,
security and test-delta reviews accept this bounded unit. The final boundary and
regression commands terminated with exit zero and discovered every selected test;
the original matrix terminated with exit one as recorded above. No full green
matrix rerun is claimed: its two completed tests are unaffected by the subsequent
test-only correction, and its affected/missing cases passed in the final run.

### Remaining integration limits

Withdrawal deliberately leaves completed history with a stopped successor and
active fence. Fresh confirmation refuses that stopped state even if roles are
restored; authorized restoration still needs integration before the production
factory can open. Active database-committed restoration, terminal-current
attestation, complete typed-adapter integration and actual Docker/process/hosted
acceptance remain outstanding. Boundary checks do not prove transaction-atomic
role revocation. The same-day Docker engine limitation above still applies;
no live Docker, Linux race, frontend build or full repository suite is claimed.

Disablement remains the closed production factory. Preserve immutable history
and recovery state when reverting code. This unit adds no managed database or
Neon provisioning, imports no unapproved branch history, and performs no remote
publication, GitHub merge or deployment.

## Authorized restoration of completed successors (2026-10-07)

This unit continues locally from `dcec59c3edf6eb629f3be541f881c96d758ea58e`.
The approved startup-authority and guarded-restart dependencies are already
retained by local integration commit `b681cf9255a4f32d53d248be32841f895095920e`;
this unit imports no additional branch history. The final source/test tree is
`205da358b28a564237074d0d073f6a000f30b005`.

### Behavior and failure boundaries

Only replay of durable COMMIT sequence17 may restore its exact stopped
successor. Before database commit, the typed runtime verifies current rebind,
configure and LAN approvals, the complete healthy runtime census, exact LAN
bindings, protected selection/history, retained resources/configuration and
listener absence. The predecessor must already be stopped. The only permitted
restart effect is `container start` with the retained immutable successor ID.
Fresh physical and authority readback must resolve even a lost acknowledgment.

After database commit, stopped recovery delegates to the existing guarded
committed-serving driver. Already-running replay remains observational and
requires the same full serving census, exact committed baseline and retained
LAN history. Its successful observation does not require opening the default
current driver factory. No path clears either process admission latch.

The shared precommit serving census does not require rollback permission:
SQL successor_ready retains COMMIT and already disallows rollback. The
predecessor rollback caller explicitly retains its rollback-permission check.
Forward recovery never reinterprets completed history as permission to roll back.
Failed restoration returns through existing ownership-only successor withdrawal;
ambiguous ownership remains potentially live. SQL, protected history, resources
and the active fence remain retained. Ordinary SQL-transition confirmation
continues to observe only and cannot restart a stopped successor.

### Verification scope

Tests use local SQLite and protected records with simulated Docker and network
probes. The no-receipt and receipt-bound tests execute the actual typed handover
through durable sequence17. The database-committed stopped case uses the existing
bounded handover fixture and explicitly injected managed current physical
executor; it proves dispatch/restart/replay, not a complete combined Docker
journey. The running database-committed cases use the actual typed fixture and
leave the default current factory closed.

The original red test refused an authorized stopped successor with no effects.
An initial implementation still refused because its cutover predicate expected
the predecessor's original running state; completed recovery instead requires
the exact stopped predecessor. The intermediate matrix then passed no-receipt
restoration, committed restoration and 13 authorization cases, but failed the
successor_ready case. That test retained a stale RollbackAllowed flag, and the
reused census predicate also required rollback permission. The corrected test
reads the actual SQL snapshot; the corrected predicate preserves the separate
predecessor rollback gate. These failed runs are retained as evidence.

Commands use `GOCACHE=Rig/.go-cache-m3`. Logs use prefix
`Rig/temp/m3-completed-restore-20261007-`.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=4m -json ./internal/generatedingress -run '^TestGatewayRebindTypedCompletedSuccessorRestoresWithoutReceipt$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=9m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedCompletedSuccessorRestoresWithoutReceipt|TestGatewayRebindTypedCompletedRestartRequiresFreshAuthority|TestGatewayRebindTypedCompletedRestartUsesCommittedAuthority)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=10m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedCompletedRestartRequiresFreshAuthority|TestGatewayRebindTypedCompletedRestartUsesCommittedAuthority|TestGatewayRebindTypedCommittedRunningReplayRequiresCompleteCensus)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=12m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedCompletedWithoutReceiptReplay|TestGatewayRebindTypedForwardOnlyWithdrawsRevokedSuccessor|TestGatewayRebindCommittedServingRestoreKeepsActiveSQLFence|TestGatewayRebindTypedHandoverRuntimeReplaysRollbackAfterFinalRemoval|TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd ./internal/appaccess
go build -mod=readonly -buildvcs=false ./...
```

The first command ran both the red baseline and initial implementation. The
matrix ran on source/test tree `5eb25a1e39b3c6046b1a21874221363f9e1cab18`, before
the final census/history guards and phase correction. Final authority and
regression runs use the final source/test tree named above. Final-tree vet,
full Go build, read-only formatting and whitespace checks pass. Independent
source and physical security review accepted this bounded restoration unit.

| Log suffix | Actual outcome | SHA256 |
| --- | --- | --- |
| `baseline.jsonl` | Expected failure on `dcec59c`: 1 test failed, 74.294s; authorized stopped successor remained stopped with no effects | `01F0DDD7E959D113C7CD61815D090BA592E55C7A145CF1D62829B0BD35430F4D` |
| `first-fix.jsonl` | Initial implementation failed the same test, 81.010s; the original cutover predicate was unsuitable for completed recovery | `0853233DCA9254DD5975C0EF56978B2AC7ED559F5E10DFBD43B37F9FFD2A7EB6` |
| `matrix.jsonl` | Mixed run: 2 tests and 13 subcases passed; successor_ready subcase and its parent failed; package failed, 359.742s | `7C3CBBE51AF2A95F8C26BE4D93264AF217571DB5B287ECA5E1CE231262C75E64` |
| `final-authority.jsonl` | Final tree: 3 tests and 17 subcases passed, 389.316s; zero failures/skips, exit zero | `58928730BABC0FBAD1057B6F00347835BEAC70186399791F7C70B7C944AFDF51` |
| `regressions.jsonl` | Final tree: 5 tests and 20 subcases passed, 474.611s; zero failures/skips, exit zero | `0F81DA5AF089A05859C7DCB1D805FF3F4B06A05E94F14ACCD974A3959F45AFBE` |

The final authority run proves approved restart, lost start acknowledgment,
revocation before/during/after restart, incomplete or unhealthy runtimes, changed
LAN head/configuration, replacement identity, both admission latches, observational
confirmation, and successor_ready recovery. It also proves database-committed
restart/replay and running replay's full census. Every case checks its exact
effects, unchanged SQL/protected history and retained fence/resources; predecessor
restart is forbidden. The focused runs discover their selected tests explicitly.

The regression run passes the existing 13 receipt-bound withdrawal cases,
7 completed-without-receipt replay cases, committed-serving fence preservation,
rollback replay after final removal, and the closed production factory. These
checks cover the altered replay expectation and the shared census/rollback
permission boundary. The two final runs total 8 tests with 37 subcases and no
failures or skips. No final full repository suite or full original-matrix rerun
is claimed; the affected cases were explicitly run on the final tree.

### Remaining integration limits

Active completed-successor restoration is now connected, but this does not
complete M3. Current-state attestation must still handle both active database
commit and terminal startup authority, including read-only terminal proof before
the commit barrier is released. Complete coordinator/typed-adapter integration
and actual Docker/process/hosted acceptance remain outstanding. Boundary checks
do not prove transaction-atomic role revocation. The same-day Docker engine
limitation above still applies; no live Docker, Linux race, frontend build or
full repository suite is claimed.

Disablement remains the closed production factories. Preserve immutable history
and recovery state when reverting code. This unit provisions no database,
publishes no branch, merges no GitHub PR and performs no deployment.

## Active and terminal current-serving attestation (2026-10-07)

This local unit continues from `9748f78e18a70ff79a2a8dae4c3de8dcbfc4a210`.
The attestation source/test tree is
`54392cd212f7d4556307b67b35abfd3fe7c035c3`; the final source/test tree is
`82e1f73daf10c8d818a768d53369579ed98b85f7`. The final delta adds only prior-first
error preservation in the two lock-release failure branches and its direct
three-case regression test. No additional branch history is imported.

### Authority and effect boundaries

The rebind coordinator's current-serving attestation now handles two phases.
Active database-committed recovery reconstructs the exact retained attempt and
receipt, validates the typed forward boundary, and requires the original commit
baseline with its complete current serving census. Terminal startup requires
no active rebind, a clear SQL fence, the exact selected current revision, healthy
runtime components, normal LAN claims and validated retained LAN history. A
legitimate newer route revision is not mistaken for the original baseline.

Both paths pin the complete Hosting snapshot, protected selection, immutable
rebind history, route-history files and latch state across two matching physical
observations. Exact stable-serving proofs retain the existing attestation digest
format. Neither observation can start a stopped container, repair configuration,
change SQL/history or clear a latch. Active attestation requires a clear commit
barrier; terminal observation permits an unchanged held barrier because the
same recovery invocation has not yet released its locks. Fail-stop still refuses
both paths. The production observer is constructed locally through an interface
that exposes observation only; the closed current/rebind effect factories are
unchanged.

Terminal recovery now owns a failure defer after selecting its exact protected
state. It covers attestation refusal and later SQL/selection rechecks. Cleanup
sets fail-stop and uses a bounded context independent of caller cancellation.
Only the retained state, terminal receipt and immutable final-container identity
can authorize stopping. Changed ownership yields a potentially-live refusal;
there is no fallback to a different target. No terminal failure reverses SQL
commit or recreates its released fence. Active commit failures retain their
existing ownership-withdrawal defer.

The lock-release helper previously replaced an existing cleanup diagnostic on
release failure. It now joins a generic release diagnostic after the prior error,
preserving potentially-live uncertainty, both release attempts, release order,
fail-stop and the retained commit barrier.

### Executable evidence

All tests below use local SQLite/protected records with simulated Docker and
network probes. The terminal positive fixture performs a real ordinary redeploy
before checking its newer current revision and then repeats startup with a fresh
Manager. The active-to-terminal test uses real SQL transitions and the concrete
managed observer for both attestations while the commit barrier remains held;
its existing handover/forward-confirmation fixture is explicitly bounded. It is
not evidence of a complete combined typed-handover/Docker journey.

The active seven-case test covers success, incomplete runtime census, approval
revocation projected after observation, two different valid physical proofs,
stopped serving and both latches. It verifies observation has no physical or
durable effects, preserves the SQL fence and never clears latches. Authority
projection fault injection does not alter the underlying user-role table.

The terminal seven-case test covers incomplete census, late LAN approval loss,
cancellation, stopped serving, replacement container identity, a changed SQL
projection after attestation returns, and a changed protected current revision.
It proves the exact stop or no effect, zero success result, truthful potentially-
live diagnostics, retained fail-stop, unchanged committed SQL/history and no
resource removal. The protected-revision case deliberately saves a valid newer
revision; cleanup must preserve it and refuse stale ownership.

Commands use `GOCACHE=Rig/.go-cache-m3`. Logs use prefix
`Rig/temp/m3-current-attestation-20261007-`.

| Log suffix | Actual outcome | SHA256 |
| --- | --- | --- |
| `baseline.jsonl` | Expected failure on `9748f78`: terminal startup refused an exact serving successor; 1 failed test, 23.918s | `88C5F87D91C1B0D939EE3F825F9CF013522924D53EFAD030135DD867BD7C06C3` |
| `first-fix.jsonl` | Initial terminal attestation/replay passed: 1 test, 31.872s | `CCEBB2808D6F220B5A0A731041FBE5834B9137C8ACA20DEF5CFCA352182D4A7C` |
| `matrix.jsonl` | 4 tests and 14 subcases passed, 165.014s; zero failures/skips | `12AA72FE0B11E7DC2478044444E8932462C1F33E48C88EEE96B18F9E233A176D` |
| `regressions.jsonl` | 5 tests and 6 subcases passed, 203.676s; zero failures/skips | `571950AFEE625A2AC27A9328162BE3D17B3026F0AC336CA1E6666F619D7B1621` |
| `release-baseline.jsonl` | Expected failure before diagnostic fix: 1 test and 3 subcases failed, 1.238s | `250C5265980D00AAD1B615E3B66CE5E32703B9DE70F7A8DF12D10D627806396E` |
| `release-fix.jsonl` | Final tree: 2 tests and 3 subcases passed, 1.012s; zero failures/skips | `ED3BBF23F548136146EE75C159ED10ECBBC7F7DD9D9BDFC8A6F252200E2A3F96` |

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=4m -json ./internal/generatedingress -run '^TestGatewayRebindTypedTerminalAttestationPreservesCommittedHistory$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=10m -json ./internal/generatedingress -run '^(TestGatewayRebindTypedTerminalAttestationPreservesCommittedHistory|TestGatewayRebindCurrentAttestationCrossesSQLCommitBarrier|TestGatewayRebindCurrentAttestationRefusesActiveAuthorityDrift|TestGatewayRebindTerminalAttestationWithdrawsOnlyRetainedOwner)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=8m -json ./internal/generatedingress -run '^(TestRecoverGatewayRebindStartupCompletesDatabaseCommittedAttemptAndReattestsCurrent|TestRecoverGatewayRebindStartupDatabaseCommitIsForwardOnlyBeforePhysicalEffects|TestRecoverGatewayRebindStartupReleaseFailureLatchesEveryManager|TestGatewayRebindForwardConfirmationFencesCommitAndRecovery|TestGatewayRebindTypedProductionDriverRemainsClosedUntilFinalAdapterExists)$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=1m -json ./internal/generatedingress -run '^TestGatewayRebindReleaseFailurePreservesWithdrawalUncertainty$'
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=2m -json ./internal/generatedingress -run '^(TestGatewayRebindReleaseFailurePreservesWithdrawalUncertainty|TestRecoverGatewayRebindStartupReleaseFailureLatchesEveryManager)$'
go vet -mod=readonly ./internal/generatedingress ./cmd/hostd ./internal/appaccess
go build -mod=readonly -buildvcs=false ./...
```

The first command produced the red baseline and first-fix logs. The matrix and
existing regressions ran on `54392cd`; the release-fix run verified the only
subsequent production change on `82e1f73`. The regression run covers six existing
commit/recovery confirmation refusals, startup completion/reattest, forward-only
database commit, release-failure admission and the closed production factory.
Together these completed runs cover 10 distinct tests and 23 subcases; the
release-failure admission test passed twice. Selected tests were discovered,
and every completed passing run exited zero. No full matrix rerun after the
two-line release fix is claimed.

Final-source vet, full Go build, read-only formatting and whitespace checks pass.
Independent source/security review accepted the attestation tree and the final
diagnostic-preservation delta. The evidence retains both reproduced failures.

### Remaining integration limits

This closes the current-attestation phase mismatch; it does not complete M3.
Complete typed stage/handover/current-driver composition, production startup
ordering (including restoration before terminal attestation of stopped serving),
and actual Docker/process/hosted acceptance remain outstanding. The same-day
Docker Desktop Linux engine limitation above still applies. No live Docker,
Linux race, frontend build or full repository test-suite pass is claimed.
Boundary checks do not prove transaction-atomic role revocation. When the closed
current effect factory cannot perform withdrawal, failure remains explicitly
potentially live and admission stays latched.

Rollback is code-only with the closed effect factories retained; preserve all
immutable history, committed SQL and recovery state. This unit provisions no
database, publishes no branch, merges no GitHub PR and performs no deployment.
