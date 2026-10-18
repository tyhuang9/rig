# M4 lifecycle coordinator implementation

Status: implementation in progress. Real lifecycle actions remain unavailable.
The [desired lifecycle foundation](m4-desired-lifecycle-foundation.md) supplies
storage and immutable history; it does not establish permission for runtime
effects. This work does not complete M4 or waive the remaining M3 physical-device
and manual screen-reader qualification.

## Intended delivery

Implement pinned application stop, start, and restart with durable desired
intent, traffic withdrawal, exact owned-container effects, and crash recovery.
Develop the complete feature in staged local commits. A read check before
deployment is one implementation unit, not complete runtime fencing.

The first supported lifecycle strategy will be generated applications. Compose
deployment compatibility must remain intact. Compose lifecycle also needs an
exact ownership and restart-policy contract: its current apply-only model and
allowed service restart policies cannot establish safe stop/start behavior.
Leaving that capability unavailable is an explicit remaining scope limit, not
evidence that all application lifecycle requirements are complete.

## Authority and ordering

- Keep desired state separate from observed deployment, route and container
  state. NULL at revision zero remains explicit legacy uninitialized intent.
- A stopped application cannot be revived by deployment, automatic deployment,
  LAN enablement, startup, rollback, or rebind replay. A running-to-running
  revision change also invalidates previously captured authority.
- Bind each accepted lifecycle job, actor, desired revision and retained
  release/configuration/plan/image/container identities durably. Do not modify
  earlier job input, release history, or operation proof bytes.
- Preserve lock order: controller owner, deployment effects, manager mutex,
  gateway OS lock, then short SQL authority reads. Acquire exclusion before the
  authoring transaction; never wait for gateway or Docker while holding the
  SQLite writer transaction.
- LAN publication must join the outer effects exclusion. Rebind must retain
  and revalidate the desired-revision census across its separate stages.
- A read snapshot is not an effect permit. Recheck under exclusion immediately
  before effects and before recording successful observation. Cancellation
  alone is insufficient: detached deployment rollback can restore routes.

## Effect contract

**Stop:** persist stopped intent, prove local and LAN traffic absent, stop only
the exactly identified owned slots without removing them, and prove stopped
state. Preserve the application's LAN sharing consent and allocated port and
leave other applications serving.

**Start:** bind explicit running intent to the retained stopped targets,
reattest ownership and configuration, start and health-check those containers,
then publish and prove serving. Missing or drifted resources require attention;
they do not authorize a rebuild, new source resolution, or application migration.

**Restart:** from running, withdraw, stop, start and republish within one pinned
operation. From stopped, verify withdrawal and exact stopped targets before
starting them. Neither path changes application-owned database contents or
implies database rollback.

Unknown effect outcomes retain an unresolved operation and block unrelated
restoration. Recovery must inspect the exact resources and retained phase before
continuing. Cancellation never silently reverses committed intent or clears an
unresolved safety fence.

## Implementation checkpoints

- [ ] Strict desired-head reads and dispatch revalidation; durable operation and
      revision binding.
- [ ] Shared exclusion and all deployment, auto-deploy, Compose apply, recovery,
      detached rollback, startup, LAN and rebind consumers.
- [ ] Route withdrawal preserving LAN settings and owned-slot stop/start without
      removal, with explicit phase evidence.
- [ ] Crash/replay/cancellation reconciliation and finite public status.
- [ ] Full acceptance, independent review and capability enablement for each
      proven runtime strategy.

### Durable acceptance and observations

The reviewed design calls for atomic creation of a queued job, desired
transition and immutable operation with exact retained runtime pins. Historical
idempotent requests must return the original acceptance before consulting busy
state or changed runtime pins. They grant no new execution permission.

Each operation uses a finite set of immutable boundary intents and receipts.
New observations require the exact current running worker attempt and desired
revision; identical receipt replay returns history independently of later job
changes. SQL must enforce the graph, component set and outcome, including when
a caller catches a rejected write and commits its surrounding transaction.

| Action and observed baseline | Required observation order |
| --- | --- |
| Stop, running or stopped | Withdraw, every component stopped, complete |
| Start, running | Healthy, serving, complete |
| Start or restart, stopped | Observe routes absent, every component started, healthy, serving, complete |
| Restart, running | Withdraw, every component stopped, every component started, healthy, serving, complete |

Stopped-baseline withdrawal is observation only. A pending start without a
proven running receipt blocks unrelated positive runtime effects even after a
job status change or controller restart. Reconciliation of that exact pending
operation still requires fresh ownership, precondition and capacity checks.

A receipt cannot authorize another command. If a receipted postcondition later
drifts, this finite model pauses rather than repeating an unjournaled effect.
Automatic repair of such drift remains a separate recoverability limit.

The storage evidence below establishes the persisted portion of this contract.
Admission, worker recovery and all effect consumers still need their own
executable acceptance evidence.

### Retained runtime boundary

The internal adapter adds retained-container inspection, guarded stop/start and
health checks, and aggregate capacity admission. It loads the exact persisted
operation and pending boundary. It does not call candidate cleanup helpers,
whose failure paths can remove containers that lifecycle operations must retain.

The worker holds deployment-effects exclusion through job-state persistence.
A live, private scope carries that admission into the adapter without acquiring
the same lock again. Missing, expired or foreign scopes refuse. Scope release
prevents new entries and waits for bounded admitted operations before releasing
the OS lock. Context propagation preserves cancellation and startup behavior.

Capacity covers the full retained component set before any stopped member
starts. Unknown, paused, restarting, dead or drifted state is not free capacity.
An uncertain start retains its reservation and pending journal. Release based
only on the durable pending fence remains unavailable until every production
path that can start or restore a workload enforces that fence.

The canonical start roster includes both pending and receipted members. A
receipted member must still be exactly running and can never receive another
start command. The complete physical roster is checked again at the command
boundary, so an uncertain member blocks its sibling. Start and reservation
release share exclusion; accounting cannot disappear under an in-flight start.
A never-attempted reservation has a separate accounting-only cleanup that
remains safe after cancellation or scope shutdown.

This stage has no public lifecycle caller. Actual Docker acceptance and the
remaining route, restoration and recovery integrations are still required.

### Dormant route projection

A separate pure projection retains every application's runtime, route, network
ownership and LAN reservation while rendering only serving apps. An all-dormant
roster retains the gateway's shared listeners and generic challenges with
deny-only responses. An unchanged serving roster produces byte-identical V1
configuration; making one app dormant preserves its neighbors' routes and
challenges. The rebind lineage supplies the existing physical challenge token.
Upgrade identity is deliberately refused by physical rendering and challenge
expectations until its distinct source contract is implemented.

Expected configuration and probe targets are not physical traffic or ownership
proofs. Production format selection, promotion and physical observations remain
later integration work. Local-only applications must not require enabling LAN
sharing to acquire lifecycle support.

## Required acceptance

Tests must cover stale or malformed authority; same-state revision changes;
concurrent stop versus deployment, route restoration, LAN enablement and rebind;
lost effect acknowledgments; partial component sets; process restart; missing or
foreign ownership; changed pins; failed health; and exact replay after later
operations. Failure must not be upgraded to success by cancellation or a command
return value.

The real Docker journey must prove target traffic withdrawal on local and LAN
routes, unchanged neighbor traffic, retained sharing consent and port, owned
containers stopped without removal, pinned start/restart, recovery after process
restart, and persistence of external database rows. Tests must assert that no
build, application migration or external database reversal is invoked.

Existing generated and Compose deployment, rebind/LAN authority, authentication
and capability controls remain required. Hosted checks from the storage-only
foundation do not prove this new behavior.

## Baseline and limits

### Initial dispatch guard: local evidence

The first local unit adds immutable app/revision/state snapshots, mandatory
DB-backed router composition, refusal before deployment preparation, and exact
revalidation before either runtime delegate. It preserves legacy uninitialized
intent without creating history. Explicit context checks after successful
callbacks close a reviewed cancellation gap; deterministic tests cancel while
returning valid data and assert no downstream preparation or dispatch.

Final Windows checks on 2026-10-18 passed:

```text
go test -json -count=1 -timeout=5m ./internal/applifecycle ./internal/runtimeexecutor ./internal/generatedexecutor ./internal/generatedruntime
go test -json -count=1 -timeout=5m ./cmd/hostd -run '^(TestRuntimeComposition|TestGeneratedComposition)'
go vet ./internal/applifecycle ./internal/runtimeexecutor ./cmd/hostd
pwsh -NoProfile -File scripts/check-generation.ps1
```

The executor suite passed 93 parent tests and 84 children; composition passed
nine parents and 33 children. All selected parents were checked for one RUN and
PASS, with zero failures or skips. Independent security review verified the
cancellation repair. JSON SHA-256 hashes:

- Executor: `1a5ac42fb5ccf954656deb7f3b0d98737e7e0726c5531156f9767959275da7fe`.
- Composition: `fc7554369838578d672f635bb2248f0b91bdc8ccaf5f7d894f7e16c44fd7a069`.

These results cover this local unit only. A callback check cannot eliminate a
race after it returns. Durable operation binding, shared exclusion, the deeper
revival consumers, and actual lifecycle effects are still required. No separate
delivery or public capability enablement is claimed for this unit.

### Durable operation storage: local evidence

Migration 039 and the internal repository now retain atomic job/desired-intent/
operation acceptance, exact component pins, and a finite intent/receipt journal.
Historical replay precedes mutable checks in the serialized job transaction.
New journal writes require the current running attempt; SQL enforces the action
graph, component barriers and outcomes. Savepoints remove partial authority
after post-write decode failures while preserving unrelated caller work.

Authority reads validate canonical records and semantic identity. Unresolved
checks include older operations; completion requires every graph proof. A
malformed matching start receipt keeps the global pending-start fence closed.
These reads require caller-held effects exclusion before their results can be
used for effects; paginated reads are not a standalone atomic snapshot.

Final checks on 2026-10-18 passed:

```text
go test -json -count=1 -timeout=5m ./internal/applifecycle
go test -json -count=1 -timeout=5m ./internal/jobs ./internal/database ./internal/runtimeexecutor
go vet ./internal/applifecycle ./internal/jobs ./internal/database ./internal/generatedingress
go build ./cmd/hostd
pwsh -NoProfile -File scripts/check-generation.ps1
```

Windows and native Linux each passed 27 lifecycle parents and 102 children.
The Windows jobs/database/dispatch regression run passed 139 parents and 89
children. Every discovered parent ran and passed once; there were no failures
or skips. Linux ran a cross-compiled test executable in WSL, with both native
and JSON conversion exits zero. Local runs were not race-instrumented.
Independent security and QA reviews accepted the repairs. JSON hashes:

- Windows lifecycle: `0b919be24f4dd65ba88ab7098e924579bf8721ccfa4be7808c62dfc6f601c4a8`.
- Windows regressions: `fcc963c95a5a39242f89c464d06c14193566b331a57ba97a4fb4041267df750a`.
- Linux lifecycle: `5389938b6a667553a02ebfa1f6b4ffec3cd7fdc17aa005d8eb1533c082946f1e`.

Migration copies are byte-identical at SHA-256
`359a0b996b5aecae943fc5750a1de58fb773a6c32912a97989ce9f35437a2265`.
Ordinary deployment bindings, startup completion repair, complete runtime and
route coordination, and public actions remain unimplemented. These storage
results do not establish those behaviors. The separate retained-runtime results
below cover its internal admission and physical-boundary adapters.

### Retained runtime boundary: local evidence

Final checks on 2026-10-18 used Go 1.26.7, readonly modules, `CGO_ENABLED=0`
and one compile worker. Every discovered parent selected below ran once on
Windows and once in a native Linux test executable under WSL:

| Package / selected tests | Windows parents / children | Linux parents / children |
| --- | --- | --- |
| `generatedruntime`, full package | 43 / 17 | 43 / 17 |
| `generatedlifecycle`, full package | 6 / 3 | 6 / 3 |
| `runtime/deploymenteffects`, full package | 11 / 6 | 12 / 7 |
| `applifecycle`, pending runtime authority and affected journal fences | 8 / 18 | 8 / 18 |
| `jobs`, scoped and legacy admitted worker | 10 / 3 | 10 / 3 |
| `cmd/hostd`, effects admission, worker preparation and emergency startup | 13 / 16 | 13 / 16 |

All tests passed except the existing Windows symlink subtest, which skipped
because symlink creation was unavailable. Its native Linux counterpart passed;
Linux had no skips. Native execution and JSON conversion exited zero. The local
runs were not race-instrumented and used deterministic Docker substitutes.

Controls include partial two-component recovery, historical-receipt drift,
foreign networks, unknown sibling state at the command boundary, aggregate
capacity refusal, lost acknowledgments, cancellation after successful physical
observations, changed worker authority suppressing proof, and concurrent
start/release exclusion. Command inventories exclude remove, create, build,
migration and environment staging. Security and QA reviews accepted the unit
after the identified recovery, accounting and test gaps were repaired.

Representative full-package JSON hashes:

- Windows runtime: `d5b945c41b3e77071cb1cf62b2ecf3e6f031a8a838185086949e475256886dcd`.
- Linux runtime: `6761cf331fb0622e1d2357029dc73b0da889cc93d9c1d97518aed0aa095d2dfb`.
- Windows adapter: `c281670a455951b992af70a147c65c780e1bc00c95bc39d9c0c117a0afaad373`.
- Linux adapter: `1631335baebb7b91413cb44b9e5ab7d2460ae6d71374babe195765fc94b6db3b`.

The internal adapter does not create jobs, intents, receipts, or routes and has
no production lifecycle caller. These checks do not establish real Docker
acceptance, crash recovery, complete restoration fencing or public actions.

### Dormant route storage: local evidence

A separate protected current-route V2 format now represents serving and dormant
applications while retaining their runtime identity and optional LAN consent.
It binds the complete expected lineage and immutable V1 source. Canonical byte
validation rejects duplicate JSON keys, case aliases and unsupported fields.
Existing V1 files, purposes, canonical bytes and digests remain unchanged.

The store is disconnected from production selection and traffic handling.
Pending transitions and LAN recovery records are deliberately rejected until
their state machines exist. Its update method requires caller-held gateway
exclusion; it is not an independently atomic filesystem compare-and-swap.

On 2026-10-18, Windows and native Linux each passed the same three parents and
30 child tests, with every selected name running and passing once and no skips
or failures. The new V2 parent accounts for 31 of those named runs. The selector
also includes both existing V1 baseline/update regression parents:

```text
go test -json -count=1 -timeout=5m ./internal/generatedingress -run '^(TestGatewayCurrentRouteStateV2Storage|TestGatewayCurrentV1TerminalBuildsAdditiveScopedBaseline|TestGatewayCurrentUpdatePreservesOriginAndTerminalReceipt)$'
go vet ./internal/generatedingress
```

Linux used the same selector in a cross-compiled test executable, run in WSL;
its native exit and JSON conversion exit were both zero. These local runs were
not race-instrumented. Formatter and diff checks passed, and independent
security and QA reviews accepted the lineage and parser repairs. JSON hashes:

- Windows: `3c2e2c0b5ea55939e18a61ef3019d1aa278953b869990fc918b7790f47d83df3`.
- Linux: `0e6f47e4f693945d0a7ceca4c602353f50cd821c181f7bd422846017ab015cbe`.

These checks establish storage behavior only. They do not prove actual traffic
withdrawal, container stop/start, protected-format promotion, rebind V3 or
controller recovery.

### Dormant route projection: local evidence

On 2026-10-18, Windows and native Linux each passed seven parents and 43 child
tests: `TestGatewayCurrentProjectionV2` and the six existing
`TestBuildCaddyConfigV2` parents. The executable selector enumerated those exact
seven names. Every selected test ran and passed once, with no failures or skips.
Linux used a cross-compiled test executable in WSL; native execution and JSON
conversion both exited zero. These local runs were not race-instrumented.

The controls establish all-serving byte equality, mixed and all-dormant
ownership retention, removal of app-specific challenges, preserved generic
port and neighboring-app challenges, local-only members, deterministic output,
defensive copies, and refusal of malformed or unsupported physical identities.
Independent security and QA reviews accepted the projection. JSON hashes:

- Windows: `f69c59bae6740348d05d2c9f06e55e185f6430682c34a30f950135757f762eb9`.
- Linux: `6d8ba1881c3d27787b8767a36a09ce9aebde9b5f161b4db6b87cd8492c4d5fb4`.

This unit performs no file selection, promotion, Docker command or network
observation. It cannot establish physical withdrawal or enable lifecycle.

### Pre-change baseline

The untouched foundation at `da607a88ee22c9af9d0a33909e94ef5531cc4f9e`
passed the following Windows baseline with Go 1.26.7, readonly modules and
`CGO_ENABLED=0`:

```text
go test -json -count=1 -timeout=5m ./internal/runtimeexecutor ./internal/generatedexecutor ./internal/generatedruntime
```

All 79 discovered parent tests and 41 children passed, with zero failures or
skips. Raw JSON SHA-256:
`fc2693e00513691a436b8ea6ec18aaaafbf6dbeb5ef0abf1eef2b173ed2a6383`.
This is the pre-change baseline, not acceptance of the coordinator.

Capability disablement remains the boundary during implementation. Preserve
additive schema, stopped intent, immutable history and unresolved evidence on
rollback. No binary-only downgrade or destructive downmigration is assumed
safe. This work does not activate a live controller data root.
