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
