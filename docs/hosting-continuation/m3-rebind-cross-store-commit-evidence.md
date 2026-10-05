# M3 cross-store commit and recovery

Status: implementation plan accepted after source and security review; clean
baseline verified. Runtime implementation is next. No publication, merge or
deployment is authorized for this branch.

Branch: `feature/hosting-m3-rebind-cross-store-commit`.
Base: `a888cac5f34c46aad74afd5c3a0392eec914c509`, the reviewed private handover
candidate. That handover's draft-publication request is pending. Its prerequisite
[draft PR #136](https://github.com/tyhuang9/rig/pull/136) is published at
`bdbb697f767da4e71664da1434a75e6daec4b732`.

## Purpose and required end state

Connect exact protected handover evidence to atomic SQL commit and recoverable
effective profile state, preserving immutable raw grants and history. The
runtime delivery plan must cover commit, phase-aware recovery, transfer-aware
readers, final physical reattestation and guarded fence release together. The
administrator API/UI remains a separate product unit. This document does not
claim that a database-committed-only foundation completes that end state.

The authoritative [cutover contract](./m3-rebind-cutover-contract.md) remains
in force. The detailed implementation plan is under source-based review before
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
   | `GatewayRebindSourceRef` | Exact source kind/operation, profile identity, protected generation/identity; real prior terminal receipt for a rebind source |
   | `GatewayRebindSpecV2` | Typed predecessor, successor/configure identity and versioned complete roster digest/count; v1 type and digests remain unchanged |
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

## Observed hosted prerequisite evidence

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

The containing workflow was still running, so the completed job's metadata and
logs were read directly. This proves those named inherited journeys only.
PR #136's two final-config-copy Docker jobs remained queued at the latest check;
private handover Docker acceptance is still pending publication and execution.
No new SQL, Linux race, rebind cutover or second-device result is claimed.

## Recovery and rollback boundary

Keep the new runtime path unavailable until its complete invariants and gates
are satisfied. Reverting source must not delete protected receipts, committed
profiles, transfer history or uncertain resources. Before a database commit,
only exact proved abort recovery can roll back. After a historical database
commit, preserve successor state and recover forward. External databases remain
application-owned through scoped runtime secrets; this work adds no managed
database or Neon provisioning.
