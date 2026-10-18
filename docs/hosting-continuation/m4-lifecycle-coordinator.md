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

The storage, admission, guarded restart-recovery, worker claim/resume, router
preparation, shared policy Gate and Compose apply evidence below establish their bounded
portions of this contract.
Final effect consumers still require their own executable acceptance evidence.

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
challenges. Upgrade and rebind each retain their original physical challenge
token and configuration derivation.

Expected configuration and probe targets are not physical traffic or ownership
proofs. Automatic production promotion and lifecycle traffic changes remain
later integration work. Local-only applications must not require enabling LAN
sharing to acquire lifecycle support.

### Protected format selection and promotion

The private reader selects V2 explicitly, and private preparation creates an
all-serving baseline with unchanged traffic configuration. Selection validates
the real protected origin, SQL-selected lineage and complete ownership
projection. Malformed, unreadable or unprojectable state refuses without V1
fallback. Legacy history and mutation paths refuse V2 until their adapters exist.

Upgrade and rebind have different origin proofs. Upgrade can legitimately have
generation zero and uses its committed journal; it has no current-route V1
source or terminal rebind receipt. The separate origin arm expresses those facts
while retaining the existing rebind wire shape and challenge derivation.

Preparation requires the existing effects and gateway exclusions, revalidates
the complete runtime and consent census, proves unchanged physical configuration,
then creates the baseline exclusively. It repeats physical observation after
creation or retry, followed by final protected, SQL, desired-head and fence
checks. Failure after file creation retains that file and refuses success;
retry validates it without overwriting history. There is no production caller.
The local verification record below covers this preparation unit.

Two further contracts remain necessary before general lifecycle actions:
versioned operation anchors that represent genuine upgrade provenance, and a
separate adoption path for gateways that have always been localhost-only. That
path must retain real local ownership without inventing a LAN profile.

### Ordinary deployment binding storage

Each ordinary deployment needs an immutable binding to its accepted desired
revision. A deployment queued before Stop must not gain permission from a later
Start. The storage unit keeps bindings separate from existing job input and
release bytes, validates historical operation evidence before current authority,
and supports historical binding reads independently of the current desired head.

Migration 040 binds eligible existing jobs to explicit revision-zero legacy
authority once. It preserves job state and history, rejects binding replacement
or deletion, and does not add an application foreign key that would prevent
otherwise valid legacy application deletion. Runtime reads never backfill a
missing binding.

New binding insertion requires exact current authority in the caller's job
creation transaction. The canonical lifecycle validator checks all present
partial graph records before distinguishing incomplete from corrupt authority;
a historical running binding requires the complete original graph. Every nested
read uses the caller's transaction. Repository/validator database identity is
checked at composition; `database/sql` cannot introspect a transaction's origin.

Guarded restart recovery and claim now preserve stale deployment evidence and
retire stale nonterminal jobs; Resume refuses stale authority. The router also
checks the original binding before preparation and delegation. Final command
checks remain separate work. These boundaries do not enable lifecycle actions
or grant runtime command permission.

### Atomic ordinary deployment admission

Manual current-source deployment, prior-release deployment and automatic
deployment now use one typed creation boundary. A new job, queued event,
immutable binding and automatic dispatch linkage commit together. Validation
or linkage failure rolls them all back; a worker wake follows only a successful
new-job commit. Production composition requires the concrete canonical lifecycle
validator and a binding repository for the same database.

An exact retry first validates its saved historical binding, then compares the
original actor, application and sealed input. It returns that job and binding
without refreshing timestamps, adding events or waking the worker, including
after desired-head drift or terminal completion. A missing binding is refused;
replay never adopts old unbound work. HTTP replay retains authentication and
existing-application access checks. Prior-release replay precedes mutable
release, strategy and capability preflight.

Automatic deployment keeps its current lease, source and dispatch identity
checks inside the binding transaction. Lifecycle refusal uses the existing
finite, sanitized deployment-failure pause; storage failure remains distinct.
This admission boundary does not replace recovery, claim, router or
command-boundary authorization.

### Guarded restart recovery

Production startup uses one `jobs.Service.RecoverRuntimeContext` callback.
It reuses the configured binding repository and holds startup admission until
recovery finishes. One SQLite writer transaction reserves the writer before
reading, collects every active deployment job, mutable deployment and selected
runtime, and classifies all bindings before changing any recovery record.
Direct linkage validation includes orphan runtimes and terminal jobs with
nonterminal runtime records; provenance filtering cannot hide them.

Current authority retains the existing recovery policy. Historically valid
stale authority preserves deployment, component and runtime evidence and retires
only a nonterminal job to `needs_attention`, once, with the fixed
`deployment_lifecycle_stale` diagnostic. Progress, checkpoints and attempts are
preserved. Missing or corrupt authority, unresolved lifecycle work, storage
failure and cancellation abort the whole transaction with distinct errors.
No binding is created or refreshed by recovery.

The service wakes a worker only after committed recovery requeues current
generated work. Startup passes its caller context, releases admission exactly
once, preserves joined recovery/release failures, and launches no worker on
failure or cancellation. The old standalone deployment and job recovery APIs
refuse deployment work; the job API retains generic non-deployment interruption.

Lifecycle-completion bookkeeping repair remains a required preceding startup
hook. This unit neither implements that hook nor performs Docker commands,
resource cleanup, route restoration or lifecycle activation.

### Deployment claim and Resume

Claim reserves SQLite's writer before reading the oldest queued job and its
original binding. Current authority permits assignment. Valid stale authority
atomically retires the job to `needs_attention` with one fixed
`deployment_lifecycle_stale` event, preserving attempt, progress, checkpoint and
runtime records. A distinct retired result allows the next eligible job to
proceed without calling an executor for stale work. Missing, corrupt or
unresolved authority leaves the job unchanged and ends that worker invocation;
it does not spin on the same row or silently skip an authority failure.

`ResumeContext` checks the saved binding, runtime approvals, requeue and event
inside one writer transaction. A failed event rolls back the requeue. Only a
successful commit wakes the worker. The HTTP endpoint propagates the request
context and returns fixed `deployment_lifecycle_stale` or
`deployment_lifecycle_unresolved` conflicts without replacing authority.

The missing-visible-application path is read-only. A raw existence check keeps
archived applications and applications with incomplete source metadata hidden
with the existing 404 response. Only a physically absent application proceeds
to historical binding validation: valid stale history produces 409 and corrupt
history produces 500. This diagnostic path cannot requeue a job. Existing actor,
capability and approval checks remain in place for visible applications.

Generic non-deployment worker behavior remains compatible. Real deployment
fixtures use typed admission and an explicit binding repository; generic worker
fixtures exercise generic jobs. These queue checks do not authorize commands
after a later desired-state change.

### Original binding at runtime preparation

The runtime router reads the saved binding for the exact job, verifies both job
and application identity, and requires an explicit current classification before
creating a deployment record or looking up a workspace. Immediately before
either runtime delegate, it checks that same original binding again. A later
running revision, including Stop followed by Start, cannot refresh an old job's
authority. Missing, invalid, unresolved, unavailable and unknown authority all
refuse; the binding is never created or replaced during execution.

Real runtime composition receives the same concrete repository used by job
admission and recovery. Missing, unconfigured or foreign-database dependencies
are rejected before runtime setup. Fake and disabled runtime modes retain their
existing behavior. The original caller context and live effects-scope values are
passed through unchanged. Cancellation is checked after successful binding
callbacks as well as before delegation.

A late refusal retains the already-created preparing deployment, job, events,
binding and runtime evidence. Typed causes remain discoverable under the
existing `runtime_unavailable` execution error; the worker currently records a
non-cancellation refusal as a failed job. This differs from the earlier claim
boundary's stale `needs_attention` retirement. No new pause disposition, route
restoration or resource cleanup is introduced here. Final command checks must
still verify current authority, current attempt and the original live effects
scope when an actual effect is about to run.

### Deployment policy guard foundation

The shared `deploymentexecution` guard keeps the original immutable binding,
job/application identity and positive worker attempt in a private Claim. The
Claim is identity only: every policy check rereads the saved binding, current
job and exact deployment through the caller's transaction. A current running
job, matching attempt, initialized provenance and an allowed policy stage are
required. The cancellation-request state is `waiting_external` with phase
`cancelling`; it cannot authorize positive work.

Policy checks require a private Pinned receiver created from the original live
effects scope. It retains the original context and exact guard. Copies share
liveness state; leaving invalidates every copy and waits for active checks before
releasing the scope entry. Cancellation and closure are checked after successful
dependencies. Construction and `UsesDatabase` require the exact configured
database, binding repository, canonical validator and admission working
directory. A SQL transaction does not expose its parent database, so the later
Gate composition must open its own transaction on that verified database.

The global positive fence is any retained lifecycle start intent without its
valid matching `container_running` receipt, regardless of job status, current
head or age. Canonical key paging, operation, intent and receipt reads all use
the caller's transaction. Other unfinished lifecycle actions retain their
existing per-application and rebind rules; they do not become a new global
positive fence. Ordinary deployment has no same-operation exception.

The foundation provides read-only validation. The bound policy Gate below adds
production consumer wiring. Concrete runtime adapters still need mandatory
current checks at each actual effect boundary, with SQL closed before external
commands. Lifecycle capability remains unavailable.

### Bound dispatch and shared policy Gate

The router constructs one Claim from the original saved binding, exact job and
application, and worker attempt before preparation. Both real delegates require
`ExecuteBound` and reject a zero or mismatched Claim before progress or
preparation. Their old unbound entry points are removed. The router retains its
two binding checks and forwards the original context, Claim and reporter.

Real runtime composition validates the original deployment and binding
repositories against the same database before setup. It constructs one guarded
deployment repository with the canonical positive-fence validator and the
owner's admission working directory, then shares it across both runtimes, the
router and generated authorization. The original read/non-Gate repository is
unchanged; calling Gate on an unconfigured repository fails closed.

Gate pins the original scope before opening its own transaction. It reserves
the SQLite writer before reading authority or policy, validates the current
binding, running attempt, deployment and global fence, and ends the transaction
before leaving the pin. First evaluation persists the complete validated finding
set. Once findings exist, retry input must match the entire semantic set:
policy version, capability, scope, fingerprint and disposition. Recovered
applying/waiting-health checks also compare empty sets exactly. Every required
approval needs one exact active tuple; rejection takes precedence.

Preparing/needs-attention retains the existing valid policy transitions.
Recovered applying/waiting-health results are read-only, including rejection
and missing approval. Authority, evidence, storage and cancellation refusals
retain deployment/runtime/component state and their typed causes. Cancellation
is checked before returning an approval or capacity wait. Ephemeral cleanup
errors join the original cause; generated authorization releases only its
unattempted process reservation. This unit does not supply final command,
cleanup or success-observation permission. Compose and generated effect guards
remain required before lifecycle activation.

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

The first local unit added immutable app/revision/state snapshots, mandatory
DB-backed router composition, refusal before deployment preparation, and exact
revalidation before either runtime delegate. It preserves legacy uninitialized
intent without creating history. Explicit context checks after successful
callbacks close a reviewed cancellation gap; deterministic tests cancel while
returning valid data and assert no downstream preparation or dispatch.
The original-binding router unit below supersedes that app-only snapshot
consumer; these earlier results remain historical evidence.

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

These results cover this initial local unit only. A callback check cannot
eliminate a race after it returns. Later sections record durable operation
binding and revival checks; shared exclusion at final command boundaries and
actual lifecycle effects remain required. No separate delivery or public
capability enablement is claimed for this unit.

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
Those storage-only results do not establish ordinary deployment binding,
startup completion repair, complete runtime/route coordination or public actions.
The later binding, admission and restart-recovery sections record their separate
evidence. Startup completion repair and full coordination remain unfinished.

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

### Protected selection and promotion: local evidence

On 2026-10-18, the protected-format selection and exclusive promotion unit
passed the following bounded checks with Go 1.26.7, readonly modules and
`CGO_ENABLED=0`:

| Check set | Parents | Children | Skips |
| --- | ---: | ---: | ---: |
| Windows legacy selection, projection, state, history and LAN regressions | 64 | 125 | 4 |
| Retained Windows selection, promotion, storage and LAN checkpoints | 4 | 86 | 0 |
| Final native Linux selection, promotion and regression groups | 68 | 213 | 0 |

Every selected name ran once per invocation and passed or took a documented
platform skip. The four Windows skips were existing permission/symlink checks;
all executed successfully on Linux. Linux ran cross-compiled test binaries in
WSL from their package directory, without race instrumentation.

Four final Windows cases separately passed after test-only review repairs:
old-format serialization/load/save compatibility, refusal of a nonterminal
upgrade, rejection of mutating commands by the observer's read-only allowlist,
and the real SQLite authority plus production observer wrapper. Their four
invocations contained nine named parent/child runs. For example:

```text
go test -json -count=1 -timeout=5m ./internal/generatedingress -run '^TestPrepareGatewayCurrentV2$/^production_observer_wrapper_uses_real_SQL_authority$'
```

The full final Linux parents include these repairs. Unchanged earlier Windows
cases were reused: the promotion checkpoint has an actual eleven-file
before/after hash receipt; selection and storage/LAN provenance uses the sole
writer's edit history and file timestamps, with hashes captured afterward.
Those earlier checkpoints are not represented as runs of later-added cases.

The fixed compatibility sample is a serializer/digest sentinel. A separate
valid dynamic pre-union fixture verifies protected raw-byte loading and
`saveNext`; this is not a fixed complete historical payload. Inspection uses a
deterministic Docker runner. Its command allowlist permits the exact config GET
and outbound config copy, and rejects reload, POST and inbound copy controls.

Representative raw JSON SHA-256 hashes:

| Evidence | SHA-256 |
| --- | --- |
| Linux final promotion parent | `4fdf77f5071476ce488370f336544597fe28b0ed265e8a1af0b2cbc83cde15cf` |
| Linux final protected selection parent | `55bbd09f758dacc86333815ffe119fe25d9f934b7f8f406209b7c5dfcebfe78c` |
| Linux final storage parent | `a8afa64f68d19b652852747dff4ca69c4a8d57ba1d7d0bf9953093d6261d31b6` |
| Windows final production observer case | `bc3f937ac104d46b7ea0718efb742f2fd92cb81db77b2806497982658a9b68c7` |

Independent security and QA reviews accepted the bounded source and repaired
tests. The controller build and canonical generation check passed at the same
ten production-file hashes before the final test-only repairs. These checks do
not prove live Docker traffic withdrawal, container lifecycle effects, recovery
activation, or full M4 acceptance.

### Ordinary deployment binding storage: local evidence

Local commit `557b570b47c5d242a00c1426de42cf858411b61a` adds immutable
binding storage and canonical transaction validation. On 2026-10-18, Windows
passed all three affected packages:

```text
go test -count=1 ./internal/deploymentbinding ./internal/applifecycle ./internal/database
go vet ./internal/deploymentbinding ./internal/applifecycle ./internal/database
```

The focused Windows JSON runs contained 13 parents and 13 children. Native
Linux covered the full binding and lifecycle packages plus the migration
parents: 43 parents and 119 children. Every selected named test ran and passed
once, with no skips. These were local, non-race checks with Go 1.26.7.

Coverage includes exact legacy seeding, fresh/upgrade/reopen behavior,
UPDATE/DELETE/REPLACE refusal with recursive triggers disabled, unchanged job,
event and release history, application deletion without binding loss, canonical
partial and complete graph validation, caller-transaction visibility, foreign
configuration refusal, corruption before stale classification, and real
repository/validator composition. Security review verified both repairs:
malformed partial records cannot become ordinary unresolved authority, and no
fallible database operation follows a successful binding insertion.

The first native migration invocation used the repository working directory,
so its relative-path mirror check failed after schema/reopen checks passed.
It passed from the package directory; the failed invocation is retained.
Removing one trailing blank line from each SQL file before commit was followed
by successful migration checks on both platforms. The canonical committed LF
bytes of each migration have SHA-256
`b9585f15450b009f91985f65f4ef259fdb9d5d4fde30128236713b4263c82a86`.
Windows checkout line-ending conversion can produce a different raw-file hash.

The Linux full lifecycle JSON has SHA-256
`11566f1bb2cd657ddaa8e1d2ece7c1cda24e1f421cac48580660b52455570215`;
the focused Windows validator JSON has SHA-256
`4e80209570c380041d6b970bc055041744abd072d17a01e05e1737ce43a7b6a1`.
Revival wiring and final command fencing remain separate work.

### Ordinary deployment admission: local evidence

Local commit `cf3b5e51d4087673b98013297c307342ad94ecfc` implements the
typed admission boundary and its controller, automatic-deployment and host
composition callers. On 2026-10-18, the Windows affected-package run passed:

```text
go test -json -count=1 ./internal/jobs ./internal/controller ./internal/autodeploy ./internal/sourceconnections ./cmd/hostd
```

It recorded 479 passing parents and 923 passing children across five packages.
One PostgreSQL relay-outage integration parent was skipped because its external
test service was not configured. Seven final review-repair parents were run
separately after test-only additions: seven parents and four children passed,
with no skips. The earlier full run is bound to a sole-writer post-run source
manifest; it is not represented as exercising tests added afterward.

Native Linux then ran all 86 selected parents and 78 children in the changed
admission and fixture test files, across six packages. Every selected name ran
and passed once; no selected test was skipped. The external PostgreSQL test was
explicitly excluded from this local selection. Source hashes were identical
before and after the run. Linux used cross-compiled binaries in WSL from their
package directories; both platforms were local and non-race, using Go 1.26.7
with readonly modules and `CGO_ENABLED=0`.

Coverage includes real completed running/stopped lifecycle graphs, queued and
terminal replay after desired drift, unchanged durable state, no replay wake,
same-key concurrent creation, stopped/unresolved refusal, transaction rollback,
saved dispatch linkage, expired/foreign lease and source/dispatch refusal, and
release-status/capability drift. The queued desired-drift case uses explicit
test-only status sequencing to construct an adversarial retained state that
normal active-mutation admission prevents. Wake counts use a private test
channel; no production testing interface was added.

Scoped vet, controller build, canonical generation and formatter/diff checks
passed at the final source. The integrated coordinator also passed its concrete
composition and ingress/SQL smoke checks, controller build, canonical generation
and documentation build. Independent security and QA reviews accepted this
bounded unit. Raw JSON SHA-256 hashes:

| Evidence | SHA-256 |
| --- | --- |
| Windows affected-package run | `25fcaa0759a46407d7b83ab34729cbb8e0349f749bc4c60f902c0eb0fdffed69` |
| Windows final concurrency/wake cases | `697922c49041c8eaf01bd6457163fcd0d9e5a426851e481b80d1426fbe3d6e34` |
| Linux typed authority integration | `a15e786b7f8135964d301f71bbc3d8809d911a897d81e825df50c0e4f123a821` |
| Linux automatic-deployment cases | `7493620cb1947320d70874d3c5c1223960dc0379853d359776f83f4c58043f5c` |

These checks establish local admission behavior. They do not establish hosted
race behavior, real Docker lifecycle effects, startup recovery acceptance,
complete M4, or activation of a live controller.

### Guarded restart recovery: local evidence

Core commit `e64511bed97508f4959f6fe66dd500d8a2ea7d37` was integrated
locally at `bbd44400032ec1d39dff68ebde2c46667d716c41`. On 2026-10-18,
the Windows core package and its native Linux executable each passed 33 parent
tests and 50 subtests, with no failures or skips. The Windows command was:

```text
go test -json -v -count=1 -timeout=10m ./internal/generatedrecovery
go vet ./internal/generatedrecovery
```

After review, five focused parents and six subtests passed on both platforms.
These test-only additions establish mixed current/stale/late-failure rollback,
terminal stale job/event preservation, orphan runtime refusal, and cancellation
after mutations. The cancellation trigger observes an already-failed current
runtime inside the transaction, then verifies that cancellation restores all
job, event, deployment, runtime and component records. The canonical validator
also runs inside the writer reservation; deterministic barriers verify that
concurrent desired-state authoring cannot interleave.

The combined service, repository and startup selection passed 17 parents
and six subtests per platform on Windows and native Linux. Coverage includes exactly one
post-commit wake for a safe generated requeue, no additional wake on repeat,
zero wakes on transaction rollback, standalone-wrapper refusal, startup context
propagation, admission release and cross-process admission ordering. The
service selection was `^TestRecoverRuntimeContext`; startup covered every
`TestPrepareRuntimeWorker` case, composition ordering and the existing
cross-process admission parent.

Controller build, scoped vet, canonical `scripts/check-generation.ps1`, and
compilation with `-tags live_docker` passed. The live tests were compiled only;
no Docker or running-controller action was performed. Initial check failures
were confined to tests: a before/after assertion compared a newly-created
object with a database-defaulted object, and a converted live fixture retained
an unused import. Both were repaired with the failed logs retained. Normal
startup tests were not repeated after the isolated live-import removal.

All final checks use Go 1.26.7 with readonly modules and `CGO_ENABLED=0`.
Core and startup sources have before/after hash receipts; native Linux uses WSL
without race instrumentation. Independent security and QA reviews accepted the
finite behavioral scope. Evidence receipt SHA-256 hashes:

| Evidence | SHA-256 |
| --- | --- |
| Core native Linux plus Windows log audit | `eb238c83b8b072d3342e6666be2b80d68d8eba2630166435e1f4a9f08bac8c6d` |
| Core finite review repairs, both platforms | `0dd890c8a5ad6111a40814f26030ca4eb916d808bfc7752b50871f92471a0828` |
| Integrated Windows startup checks | `e5c7fa12fff5bb85bf501492989cd3c64286d4390c07787e315abb1817aa10eb` |
| Integrated native Linux startup checks | `e550de2c8a4baaf23fc00a63fecfb32ce73577bde6fde2aedf5332072a4fd830` |
| Final live-fixture compilation and canonical check | `76694b430dabf7a9b49c764556141be4c51a5c355e93b180343ebf6a3f4a9012` |

These are local restart-recovery results. The claim/resume unit below supplies
the remaining jobs fixture migrations and broader jobs/controller checks.
Final runtime command fencing, lifecycle-completion repair, hosted race checks,
actual Docker lifecycle behavior and full M4 acceptance remain separate
requirements.

### Deployment claim and Resume: local evidence

Commit `ecb5e135f96f0dd509cf981bb94dfc06ec1cffc2` was integrated locally
at `c28d5abbdced9991ab9ce99709863f27a110fb93`. On 2026-10-18, the final
Windows jobs/controller run passed 238 parents and 565 subtests with no failures
or skips:

```text
go test -json -count=1 -timeout=5m ./internal/jobs ./internal/controller
go run ./cmd/openapi-gen -check
go vet ./internal/jobs ./internal/controller ./internal/generatedexecutor
go build ./internal/jobs ./internal/controller ./internal/generatedexecutor
```

The earlier three-package run also passed all 37 generated-executor parents and
16 subtests. It preceded the final jobs/controller visibility repair and remains
recorded as such, rather than being relabeled a full-suite run at the final
source. The changed real capacity-resume fixture was exercised again on native
Linux after that repair.

Native Linux ran all 80 parents and 85 subtests from the nine changed test files,
with zero failures/skips: jobs 56/29, controller 23/56 and generated executor 1/0.
The independent harness verifies every selected parent was discovered and passed
once and that all 15 frozen source hashes stayed unchanged. Tests cover stale
retirement, next-job progress, no attempt increment, corruption before stale
classification, unresolved one-shot refusal, atomic Resume rollback, preserved
visibility and approval-revocation serialization. Cancellation is tested with a
pre-canceled request; event-trigger failure separately proves rollback after the
requeue update. Synthetic lifecycle validators isolate jobs-package gate logic;
the canonical graph coverage is recorded in the earlier binding/recovery units.

After integration, the startup/wrapper selection passed 16 parents and six
subtests on Windows. The controller build and canonical
`scripts/check-generation.ps1` also passed. All source blobs from the reviewed
claim/resume commit match the integrated tree. Independent security and QA
reviews found no remaining issue in this bounded scope.

| Evidence | SHA-256 |
| --- | --- |
| Final Windows jobs/controller JSON | `03f29062e4c4192a5573b78dd779d03c01bca595b3c8a4475d796d800be9cfad` |
| Focused final visibility/rollback JSON | `ebac7e26fafc13b6a0b69ba19d946af845041fb1ea51b1128348625ecd47a0b2` |
| Native Linux source freeze | `c47abe79cddca8fca880ebd0bef96b40b64a6732176459409d642fdb6e7051a6` |
| Native Linux results receipt | `16f8350293c432eca492cc0ed3290955aee5cd76d3c3942a83824d96de4525ad` |
| Combined startup/claim integration receipt | `4eabe436eb2316822b4fdafbf5ea8febeb5cfdbcc02263f74441ea935e19f9b9` |

Checks use pinned Go 1.26.7 with readonly modules and `CGO_ENABLED=0`.
Native Linux runs through WSL without race instrumentation. No Docker command,
live-controller activation or hosted lifecycle acceptance is established by
these results. The router unit below records preparation checks; final runtime
command fencing remains required before public lifecycle activation.

### Original binding at runtime preparation: local evidence

Router commit `3de2ede4e04d0d92aed58b52dff7781403f89d44` and the
cancellation follow-up `04bb94f76d1019a4a81c58bf77e195bb67e7eed9` were
integrated locally, with final source at
`aa170b754ea7c11b7fffe9d7e5487ec1f404146e`. On 2026-10-18, the initial
Windows router suite passed 11 parents and 36 subtests; the finite hostd
selection passed eight parents and 39 subtests. Native Linux then passed all
24 parents and 82 subtests from both changed test files: router 11/36 and
hostd 13/46. Every selected parent was discovered and passed exactly once,
with no failures or skips.

Final inspection found missing cancellation checks after successful binding
callbacks. Four new Read/first-Check cases across Compose/generated reproduced
the regression before repair. Six production lines restore the cancellation
checks. The final full router package passed 12 parents and 40 subtests on both
Windows and native Linux. The other nine source files stayed byte-identical,
so their passed checks were retained. Security and independent source
review accepted the finite repair.

The broader combined claim/recovery/router selection passed 34 parents and
95 subtests before the cancellation follow-up. After that follow-up was
integrated, two composed-runtime parents and nine subtests passed, along with
the controller build and canonical generation check. Reviewed source Git blobs
match the integrated tree. These results are recorded separately so the earlier
combined run is not presented as a run against the later source.

```text
go test -json -count=1 -timeout=5m ./internal/runtimeexecutor
go vet ./internal/runtimeexecutor ./cmd/hostd
go build -o <evidence>/hostd-windows-amd64.exe ./cmd/hostd
go test -c -tags live_docker -o <evidence>/hostd-live-docker-windows-amd64.test.exe ./cmd/hostd
pwsh -NoProfile -File scripts/check-generation.ps1
```

The first scoped vet and live-fixture compilation preceded the cancellation
follow-up. Router vet, the final controller build and canonical check passed
again after the repair. Exact finite hostd selections, commands, raw logs and
source manifests are retained in the evidence receipts.

Real canonical graph fixtures cover legacy and completed running authority,
same-state Restart and Stop/Start drift at initial and late boundaries, corrupt
historical authority, wrong identity, typed failures, original context values,
and retained preparation records. Tests also exercise correct repository
injection through the assembled controller: an unbound job refuses before
preparation, and an accepted binding reaches normal Compose validation. Existing
routing/provenance and disabled/fake runtime behavior remain covered.

| Evidence | SHA-256 |
| --- | --- |
| Initial Windows router JSON | `52ea6d1cb1ced2b480726f56ed71cd5744f2e8d2151bb4f067a53b955bbf6ca4` |
| Windows composed-runtime selection JSON | `544e296dd50ec6acf2aa5e65f6e5a86f84fdd999762a39b67936cda287cb9012` |
| Initial native Linux receipt | `0d7b1e8cea9563f8fc81d982f4068a73751e21698612a76f2801735bb05c877b` |
| Expected cancellation regression JSON | `87be7e2797ed8bb9a3cdf6dfb114370a9b0530c7cc5d38c70d13d4bb20929bde` |
| Final cancellation repair, Windows/Linux receipt | `e1722719c752e6d2aa7002d410ff9534a6296b0d3dfdba7174de5e966e9f4717` |
| Final composed integration/build/canonical receipt | `f31df0d314f8532a65cef6b493fe133405b60d8c7f6ad0b7e21592ec19732c28` |

All checks use pinned Go 1.26.7 and readonly modules. Native Linux checks run
without race instrumentation. Live fixtures were compiled, not executed;
Docker behavior, final command authorization and full M4 acceptance remain
unverified. The public lifecycle capability remains disabled.

### Dormant deployment policy guard: local evidence

Guard commit `ca109c79f7a72225a2bbe01b5ec69db8e9d0bc52` was integrated
locally as `e5ffb0c675784f743d5ab642448b4dd74ae84995`. Both committed trees
are `09d96ed65593cb1342c99954373792b0f678abd8`; the coordinator documentation
is recorded separately. The seven reviewed source/test files were unchanged
through final Windows and native Linux verification on 2026-10-18.

Windows passed 21 parents and 34 subtests: seven canonical lifecycle parents
with two subtests and the complete guard package's 14 parents with 32 subtests.
Native Linux passed 24 parents and 38 subtests: ten lifecycle parents with six
subtests and the same complete guard package. The Linux selection also includes
existing journal-barrier and pending-runtime authority tests affected by the
private query factoring. Every selected parent was discovered and passed once;
neither platform had a failure or skip.

Real SQLite and canonical lifecycle fixtures cover legacy and completed running
bindings, stale same-state and Stop/Start history, invalid history before drift,
exact job/deployment identity and attempt, all accepted policy stages, and fresh
checks after earlier success. Canonical fence tests cover transaction-local key
pages, operations, intents and receipts; starts after terminal jobs and later
heads; valid matching receipts; malformed/orphan authority; and exclusion of
non-start unfinished actions from the global fence.

Scope tests cover zero, copied, foreign, released and closing entries, concurrent
Leave, outer lease release ordering, original context retention and cancellation
after successful callbacks. Narrow doubles establish distinct unresolved/storage
error mapping and callback cancellation. They do not claim that corrupt saved
completion is canonically unresolved: the existing history-first contract
classifies it as invalid. Security and QA independently accepted the frozen
source and retained evidence.

```text
go test -json -count=1 -run '^(TestPositiveFenceValidator.*|TestApplicationLifecyclePendingStartPersistsStatusPauseAndReopenUntilReceipt|TestApplicationLifecyclePendingStartRejectsMalformedMatchingReceipt)$' ./internal/applifecycle
go test -json -count=1 ./internal/deploymentexecution
go vet ./internal/applifecycle ./internal/deploymentexecution
go test -count=1 -run '^$' ./internal/applifecycle ./internal/deploymentexecution
pwsh -NoProfile -File scripts/check-generation.ps1
```

The `-run '^$'` command is compilation evidence only. Initial exploratory
fixture failures concerned Windows private-directory setup and immutable-row
test setup; those direct tool observations were not saved as raw logs. Final
numbered commands have retained raw output, exact commands, exit files and a
source manifest. The native runner records its exact parent selection and
source hashes before and after execution.

| Evidence | SHA-256 |
| --- | --- |
| Seven-file source manifest | `e8099be1d9ab67d64d918ad73720c7c0f3baff970e35ced5f53e5117848b89ae` |
| Windows lifecycle JSON | `a5e4ac3286e8826c7b5f3b6d49e69d2f02c2d99d100114cdc1d35c1df68fe5bc` |
| Windows guard JSON | `53959cdd9214ccedd48e5e5f7ebe035a1ba9692c77eaa439ad10a5e0a2285e0b` |
| Native Linux source freeze | `36f8e850f4613da980b8857a4f5be584a0f3a6bd4ec03f757a542ecde5bb44fd` |
| Native Linux receipt | `8270513dbab48cae31aa98930ae55f5dff37aa6b932f6c94446e00013601a049` |

All checks use Go 1.26.7 with readonly modules. Linux execution is native and
non-race. No Docker, hosted CI, production Gate/command wiring or lifecycle
activation is claimed by this dormant unit.

### Bound dispatch and shared policy Gate: local evidence

Source commit `f5efff492eae96821c5d3b05b8605ed92bd13c1e` was integrated
locally as `b955842b09cdaff7ac92c66027e5936ec936dfbb`. Both committed source
trees are `35db1a73f5f483171246dedfbae5541c56253978`. The 24 reviewed files
were unchanged through final verification on 2026-10-18.

Final acceptance used CI's pinned Go 1.26.7, readonly modules and
`CGO_ENABLED=0`. The four affected packages were run in full; all non-live test
parents from the changed jobs, controller and hostd fixture files were also
selected. Every selected parent was discovered exactly once.

| Platform | Passed parents | Passed subtests | Skips / failures |
| --- | ---: | ---: | --- |
| Windows | 137 | 329 | One symlink-privilege parent skip; no failures |
| Native Linux through WSL | 138 | 329 | None |

Linux passed `TestPolicyRejectsSymlinkAncestor`, covering the Windows skip.
The checks cover exact Claim forwarding, same-database and scope rejection,
complete retry/recovery policy sets, exact approval freshness, cancellation
before policy classification, original read-error causes and retained state
after ordinary non-cancelled refusals. Real SQLite begin/insert/update/commit
faults prove rollback; the commit fault uses a deferred foreign key and fails at
Commit rather than at insertion. Existing secret, configuration, source,
approval/resume and capacity contracts remain exercised by real router/scoped
admission fixtures. Security, QA and integration review found no unresolved
source issue.

```text
go test -json -count=1 -timeout=5m ./internal/deployments ./internal/runtimeexecutor ./internal/composeruntime ./internal/generatedexecutor
go vet ./cmd/hostd ./internal/composeruntime ./internal/controller ./internal/deployments ./internal/generatedexecutor ./internal/jobs ./internal/runtimeexecutor
go vet -tags live_docker ./cmd/hostd
go test -c -tags live_docker -o <evidence>/hostd-live.test.exe ./cmd/hostd
go build -o <evidence>/hostd.exe ./cmd/hostd
pwsh -NoProfile -File scripts/check-generation.ps1
git diff --check
```

The receipts retain actual per-package commands, anchored fixture selections,
toolchain path/environment, raw output, exit codes and source hashes before and
after execution. Native Linux checks are not race-instrumented. Live Docker
fixtures were compiled and vetted, not executed.

Earlier supplemental Windows checks used Go 1.27.0: four full packages passed
92 parents and 212 subtests with the same symlink skip, while an overlapping
focused selection passed 32 parents and 92 subtests. An initial metadata error
in their summary was corrected in a separately retained summary. The final
Go 1.26.7 runs above establish CI-toolchain compatibility. Earlier fixture and
compile failures remain recorded. Two live-only fixture compiler fixes advanced
the source freeze from v1 to v2; the final checks use v2.

| Evidence | SHA-256 |
| --- | --- |
| Reviewed source v2 manifest | `a0c4c00f672da9f1255db089c55693b7e163b18c9311fae7c52c7e8889386523` |
| Reviewed source v2 patch | `9132b60c90c2463cd6722fb12f13ee1379bf467f4e3fa1dde7ed6abcd5de7fc2` |
| Root source/test freeze | `01e0d5fc3b54f5e24389564bd003c199fc78d95a5325f126bc0acc1fdb0f8fcb` |
| Go 1.26.7 Windows receipt | `81f054ac509235b3e19ecbdc4a2e9fa3a62262588206e756834e17af8eba38d4` |
| Go 1.26.7 native Linux receipt | `86923e198bf243af69bf7f612f3df3c3c5d01dbdedba04b2266ba55407d1a86c` |
| Corrected supplemental Windows summary | `58807da6cfa8052a873dd2f133efb3d5075370f9a44b8fe94b5d39003144b441` |

Final command freshness, generated recovery shortcuts, cleanup authorization,
startup completion repair and actual Docker/race acceptance remain separate
work. This local integration does not enable lifecycle capability or activate a
controller data root.

### Compose apply and success observations: local evidence

Source commit `e3fba8ea57cb6b35e796daa50dd5da88d99f7139` was integrated
locally as `cfefb489dcedeb19911e1b6b7c3c377ce02641d5`. Both committed source
trees are `2df5f480d63369bc01044cca79cdd7a8402b3102`. All ten reviewed
source/test files remained unchanged through final verification on 2026-10-18.

The fixed Compose adapter retains the original Claim, inspected protected
configuration, release, configuration revision and complete finding set. It
builds the command before the final fresh authority/policy check, then invokes
exactly that `up` request. Only `applying` can authorize `up`; an acknowledged
`waiting_health` deployment cannot use this path to repeat the command.

The original effects scope remains pinned through the command, progress
reporting, protected-file cleanup and completion. Separate repository-owned
transactions revalidate current authority and exact policy before recording
`waiting_health` and `succeeded`. Observation writes reserve the SQLite writer
before authority reads. SQL never spans the process runner, reporter or cleanup.
Unknown execution, lost acknowledgment, cancellation and observation failures
retain the relevant durable state; they do not trigger Docker rollback or grant
same-job retry permission. Original failure and cancellation causes are retained.

Final checks used pinned Go 1.26.7 with readonly modules, `-p=1`,
`-buildvcs=false` and `CGO_ENABLED=0`. Guard, deployment repository and Compose
packages were run in full. The finite Windows selection additionally ran all ten
parents from the changed jobs fixture and six relevant controller composition
parents. Native Linux ran the three full packages and those ten jobs parents.

| Platform and scope | Passed parents | Passed subtests | Skips / failures |
| --- | ---: | ---: | --- |
| Windows, three full packages | 69 | 208 | One symlink-privilege parent skip; no failures |
| Windows, jobs/controller selection | 16 | 33 | None |
| Native Linux through WSL | 80 | 215 | None |

Every selected parent was discovered exactly once. Linux explicitly passed
`TestPolicyRejectsSymlinkAncestor`, covering the Windows skip. Tests cover stale
attempts, binding/provenance drift and approval revocation before `up` and at
both observations; applying-to-succeeded refusal; actual SQL statement, CAS and
deferred-constraint commit failures; copied/closed scopes; cancellation causes;
and a blocked runner with an independent successful database writer.

The worker regression acknowledges one `up`, injects a finalize-write failure,
then runs actual restart recovery and another empty claim iteration. It verifies
the same failed job/attempt and deployment linkage, with no second `up`.
Original configuration-resume, source diagnostics, nested Compose paths, managed
read-only bind, policy and failure classification assertions remain covered.
Security, QA and integration review accepted the frozen source and raw evidence.

```text
go test -json -count=1 ./internal/deploymentexecution ./internal/deployments ./internal/composeruntime
go vet ./internal/deploymentexecution ./internal/deployments ./internal/composeruntime ./internal/jobs ./cmd/hostd
go vet -tags live_docker ./cmd/hostd
go test -json -tags live_docker -count=1 -run '^$' ./cmd/hostd
go build -trimpath -o <evidence>/hostd-windows-e3-verification.exe ./cmd/hostd
pwsh -NoProfile -File scripts/check-generation.ps1
git diff --check
```

Read-only `gofmt -l` on the ten frozen Go files returned no paths. Receipts
retain the exact anchored jobs/controller selection, toolchain path/environment,
raw JSON, exit codes, test discovery and source hashes. The initial Compose
compile failure is retained: its old fake deployment fixture did not satisfy
the new concrete guarded repository constructor and was migrated. Earlier
focused checks are supplemental, not evidence for the later frozen source.

| Evidence | SHA-256 |
| --- | --- |
| Ten-file source manifest | `6b73a68e9ce19dd9ac26321f2638b9bd4a15ece81614ce20b2af749540ad40dd` |
| Reviewed complete patch | `2c50a6ec40ef24ff33d9eb1d37deb00da36ad1061159dea46d4a7797d0c4e3c9` |
| Go 1.26.7 Windows summary | `887ffdf364abea8f0931e038049c046eb90c3a5927bce8d9ddbebf68b7a079f4` |
| Native source/test freeze | `64efc6373f5fb51bf2ca2a9dc1afb014c16bfc648cccdfacc1a481685b704ffc` |
| Go 1.26.7 native Linux receipt | `496e57bcab4d6ea84e91e9bb13c928f394d3fb537d550dec22ce115a2256cd8e` |

Native Linux is non-race. The live Docker command above compiled the fixture
with zero tests selected; it establishes no Docker execution. Actual Docker and
hosted race gates remain required on the published draft head. No controller
activation or manual deployment was performed.

Compose still has no durable per-command intent/result receipt or general
exactly-once guarantee for ambiguous `applying` state. Full Compose ownership
and restart-policy lifecycle support remains unfinished. Generated command
families, route/restoration/drain authority, startup repair and public lifecycle
enablement also remain separate work. This accepted unit does not complete M4.

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
