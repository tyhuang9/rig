# M3 cross-store commit and recovery

Status: implementation plan accepted after source and security review; clean
baseline verified. Database and runtime implementation is underway. No publication, merge or
deployment is authorized for this branch.

Branch: `feature/hosting-m3-rebind-cross-store-commit`.
Base: `a888cac5f34c46aad74afd5c3a0392eec914c509`, the reviewed private handover
candidate. That handover's draft-publication request is pending. Its prerequisite
[draft PR #136](https://github.com/tyhuang9/rig/pull/136) was initially published at
`bdbb697f767da4e71664da1434a75e6daec4b732`; its corrected current head is
`b46162ea4a97335976663770b17d2a44612cc3f9`.

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

This is a design decision awaiting implementation and adversarial tests.
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

PR #136 received the separately reviewed CI partition correction at `7deefa4`.
Its M3 Docker workflow passed again at that exact revision (run `37391508257`).
The previous repository-wide race job at `b46162e` was cancelled by normal
workflow concurrency after the update; its PostgreSQL-specific steps passed,
but its repository-wide result is unverified. New race batches at `7deefa4`
all passed, including the aggregate check (run `37391508420`). Windows passed
at the same head (run `37391508875`). The repository-wide race step in run
`37391508290` remains pending. The published correction is integrated locally
at `3cf6bb8`.

Keep the new runtime path unavailable until its complete invariants and gates
are satisfied. Reverting source must not delete protected receipts, committed
profiles, transfer history or uncertain resources. Before a database commit,
only exact proved abort recovery can roll back. After a historical database
commit, preserve successor state and recover forward. External databases remain
application-owned through scoped runtime secrets; this work adds no managed
database or Neon provisioning.
