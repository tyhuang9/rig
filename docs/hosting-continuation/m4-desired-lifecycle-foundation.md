# M4 desired lifecycle foundation

Status: local verification passed on 2026-10-18; hosted verification pending. This increment adds
internal storage for desired application intent. It does not enable application
start, stop, or restart, and it does not complete M4. M3's physical second-device
and manual screen-reader qualification remain open.

## Purpose and boundaries

The [M0 integration baseline](m0-integration-baseline.md) requires desired
running/stopped state to be separate from observed Docker and route state.
This branch starts from the accepted runtime source
`4200796e3e41450e192368976b64089c84445d90` in
[PR #186](https://github.com/tyhuang9/rig/pull/186). Its hosted M3 evidence is
recorded separately; it is not evidence that this new storage change passed.

Existing and new applications begin with explicitly uninitialized intent:
`desired_state = NULL`, revision zero, and no transition history. This does not
infer running or stopped from an application status, deployment, route, or
container observation. Uninitialized is an internal representation, not a new
user-requested lifecycle mode.

The repository contract is:

- Read current desired intent separately from immutable transition history.
- Author only within a caller-owned SQL transaction, using a persisted job and
  its application, action, actor, and expected intent revision.
- Derive running from start/restart and stopped from stop. Same-target
  transitions are allowed when they represent a distinct accepted operation.
- Advance the head by one revision with the corresponding history insertion.
  A failed guarded insertion must leave both unchanged, even when its caller
  catches the error and commits other work.
- Return the saved historical transition for an exact replay. Replay does not
  advance the head or claim that the saved transition is the current head.
- Reject stale revisions, conflicting operation reuse, and history rewriting.

Migration 038 initializes applications transactionally through an insert trigger.
The history insertion and head advance are one guarded SQLite statement. A local
savepoint also removes intent if result decoding fails after that statement.
Each cleanup statement has an independent five-second context; failure
invalidates the caller transaction. `database/sql` has no context-aware
`Tx.Rollback`, so a wedged driver prevents a strict overall cleanup time bound.

Replay checks the retained transition before consulting mutable job metadata.
New operations must match the persisted job. SQL guards reject history updates,
deletion, replacement, head reset, and history loss through application deletion.
An uninitialized application's normal deletion remains possible. Integrating a
future application deletion policy with retained lifecycle history is separate
work.

No production caller authors intent in this increment. The real lifecycle
endpoints remain capability-gated. There are no public API/DTO, UI, runtime,
ingress, dependency, or CI workflow changes. The repository performs no Docker,
route, build, application migration, or application-owned database effects.

## Verification record

The untouched base passed the selected database, application creation, job
transaction/idempotency, and controller capability baseline: 11 repository
parent tests and two controller parents with five children. The canonical
`scripts/check-generation.ps1` check also passed. These are baseline results,
not results for the new implementation.

The final source passed these checks with Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`, and `CGO_ENABLED=0`:

```text
go test -json -count=1 -timeout=5m ./internal/applifecycle ./internal/database ./internal/apps ./internal/jobs ./internal/controller
go vet ./internal/applifecycle ./internal/database ./internal/apps ./internal/controller
pwsh -NoProfile -File scripts/check-generation.ps1
```

- Windows: five packages, 290 parent tests and 608 children passed, zero failures
  or skips. All 11 new parent tests were separately checked for one RUN and PASS.
- Linux: the cross-compiled `internal/applifecycle` test binary executed natively
  in WSL; all six parents passed. The two-database-handle CAS test also passed
  20 repetitions. This is native Linux execution, without race instrumentation.
- Fresh schema, upgrade from 037, repeated migration, and close/reopen controls
  preserve application status and existing jobs, events, and releases. Existing
  repository readers still pass; an older controller binary was not exercised.
- Failure controls cover malformed and mismatched authority, stale revision,
  conflicting and historical replay, cancellation, post-write decoding failure,
  caller rollback, and direct SQL replacement/history mutation attempts.
- Authenticated controller requests verify all three real actions still return
  `capability_unavailable`; fake actions queue jobs without authoring intent.
- Read-only formatting, migration byte equality, and `git diff --check` passed.
  Documentation build results and the published source identity are recorded in
  the accompanying PR.

Retained final evidence hashes (SHA-256):

| Evidence | SHA-256 |
| --- | --- |
| Windows affected-package JSON | `7cbab12d923f3d01b56f38921357fb14827b14c52bd3afc01152a25b137541d7` |
| Linux lifecycle JSON | `2857465971fe74eaafe0bad3827450f7bbfc076886a2c4fbdf03a60b439a9450` |
| Linux concurrency JSON, 20 runs | `d4effdb2e36300de83d020eb2764fcee69a17878d7ac8dbf82fc3a700a35b2b5` |
| Each migration 038 copy | `91a675113712db1688bf829626047cd343854cc4b7494a40dbdf32b83f463ff6` |

The first Linux invocation was rejected by argument parsing before any test ran;
the corrected subprocess invocation above preserves the native exit code. Prior
candidate results are retained separately and are not substituted for these
final results.

Independent security and QA reviews found and verified fixes for replay
authority, error atomicity, bounded cleanup, and missing failure coverage.
CodeRabbit CLI 0.9.0 reports `not_authenticated`; no CodeRabbit review ran.
Hosted Linux race, Windows, and existing integration gates subsequently passed
at the exact foundation revision recorded below.
The existing repository-remainder race lane discovers the new package through
`go list ./...`; no CI workflow change is needed.

### Hosted foundation verification

On 2026-10-18, [draft PR #188](https://github.com/tyhuang9/rig/pull/188)
completed CI at `da607a88ee22c9af9d0a33909e94ef5531cc4f9e`: 129 check runs
succeeded. The documentation deployment job was skipped by its existing
main-push/manual condition. The CodeRabbit status context also reported success;
that status is not evidence of an independent code review.

Passed gates include Windows, Linux repository race checks, ordinary Docker
deployment, controller TLS, two-application LAN, external-database TLS,
migration uncertainty, and all eight owned crash-matrix partitions. The
[matrix aggregate](https://github.com/tyhuang9/rig/actions/runs/39138691587/job/117745384859)
confirmed that every partition succeeded. The PR description retains links to
the individual important jobs.

The completion receipt records rollup SHA-256
`ea230c2312faee6032fa2a54023801d53f1de405330826720af31a504fcf74cd`
and aggregate metadata SHA-256
`79c12463dddef4b1afd7955ba006d754dc6cdc7c4101eaec2f5982207153adc7`.
These results cover the published storage foundation. They do not qualify the
unpublished lifecycle coordinator, physical-device acceptance, or full M4.
The PR remains a draft; this work did not merge it or activate a local live
controller data root.

## Follow-on requirements

Before enabling real actions, every deployment, gateway/LAN, startup, recovery,
and cancellation path that can restore a route or container must honor the
desired-state authority. Stop must establish traffic withdrawal before stopping
exactly owned slots; uncertain outcomes must remain visible and recoverable.
Start/restart must reattest retained release, configuration, image, and ownership
pins without rebuilding, rerunning a migration, or implying external database
rollback. Those effectful coordinators and their crash/replay acceptance are
outside this storage increment.

## Rollback and operational limits

The schema change is additive and retains transition history. Use a backed-up
controller data root for any later operational upgrade; do not assume a
destructive downmigration or binary-only rollback is safe. Compatibility tests
for older readers do not qualify a live deployment or an older controller
binary. No live controller data root is migrated or activated by this work.
