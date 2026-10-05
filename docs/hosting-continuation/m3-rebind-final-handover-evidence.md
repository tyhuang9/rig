# M3 private final-container handover

Status: private coordinator, protected terminal history, production Docker
driver, fault/restart tests and standalone Docker journeys implemented.
The initial source checkpoint is `c2d3daf5c5d8810005fadacccbda8958b4a0f3a5`.
The Caddy autosave correction is implemented and independently reviewed in
`8b61552` and `d8017e5`. The frozen full suite and separately recorded correction
checks passed. This branch is local and unpublished. Actual Docker acceptance
is pending.

Branch: `feature/hosting-m3-rebind-final-handover`.
Base: [draft PR #136](https://github.com/tyhuang9/rig/pull/136),
`feature/hosting-m3-rebind-final-config-copy` at
`bdbb697f767da4e71664da1434a75e6daec4b732`. That prerequisite was published
with explicit authorization; this handover still requires its own publication
approval. No merge or deployment is authorized.

## Purpose and boundaries

Complete the private Docker handover from verified sequence twelve through
an immutable successor commit or abort receipt, including its directly
coupled Docker tests in this branch. Preserve existing runtime safeguards,
immutable source grants/profiles and all earlier protected bytes.

SQLite remains `prepared`; the original predecessor remains current and the
rebind fence stays active. No public caller, normal startup admission,
successor URL publication, SQL transition or transfer-aware resolver is added.
A protected terminal receipt alone cannot synthesize an upgrade journal,
advance the predecessor, or admit another rebind intent. External databases
remain application-owned and configured through scoped runtime secrets.

The stage serves its existing challenge/404 configuration. Sequence twelve
retains the complete application configuration as an inactive file. Stage and
final share the successor ingress address, so handover first withdraws and
removes the exact bound stage. After stopped final creation and durable ID
binding, a separate cutover intent authorizes predecessor stop followed by
final start; final must take over the predecessor's loopback publication.
Full application-route proof occurs on the final container.

## Ownership and interface

The coordinator owns the deployment-effects lease, Manager mutex, gateway OS
lock, current prepared SQL/runtime-head checks, protected writes and recovery
direction. The driver owns fresh physical inspection and the narrow Docker
effects. Both remain private.

| Owner | Files and responsibility |
| --- | --- |
| Coordinator implementation | New handover coordinator and terminal receipt files; `gateway_rebind_progress.go`, `gateway_rebind_protected_intent.go` and `upgrade_history.go` extensions |
| Driver implementation | New `gateway_rebind_final_handover_driver` files; shared context/plan/final-binding/observation types, pure argument builders, exact topology and mutation checks |
| Independent QA | New handover tests and standalone live journeys; directly coupled hosted workflow gates after interface agreement |
| Orchestration | This evidence and the cutover contract clarification; integration and security review |

The agreed driver embeds the existing final-config-copy driver for strict
sequence-twelve admission and adds joint observation, final creation, exact
stage withdrawal, exact predecessor stop/restoration, exact final start/stop,
and removal of the bound successor resources. The context carries the
protected intent, retained sequence-twelve record, original predecessor,
source route state, current phase, handover plan and optional bound final.

An ephemeral `CreatedFinalID` is allowed only for immediate readback following
a successful create return in the same invocation. It authorizes neither a
mutation nor later adoption. A fresh process obtains the final ID only from
valid protected progress. Unknown, extra or unbound resources remain fenced;
neither a name nor matching labels permits adoption or deletion.

The initial sequence-twelve observation supplies freshly validated application
network IDs for the handover plan. Later observations require those pinned
IDs. Observations distinguish absent, stopped and running containers, exact
resource presence, original-address classification and separate config,
route, predecessor-stop, host, inventory and endpoint proofs. Two full
normalized observations must agree around each boundary. An exactly owned
running final with failed route proof remains distinguishable from uncertain
ownership so safe compensation can be considered.

Generic older-phase validators remain strict. The new joint observer proves
the explicit predecessor, successor and application endpoint sets after final
joins application networks; it does not excuse arbitrary extra endpoints.

## Protected transition policy

Sequences one through twelve retain their JSON bytes and digests. Later records
keep the exact sequence-twelve stage payload and append a separate optional
handover payload after the existing progress digest field. That payload adds
plan, final binding, cutover proof, serving proof, rollback intent and outcome
monotonically. Rollback retains historical proofs instead of rewriting them.

| Success sequence | Phase | Required evidence or permitted effect |
| --- | --- | --- |
| 13 | `final_handover_intent` | Fresh sequence-twelve admission; exact final arguments/config/ownership and application-network IDs pinned; authorize bound stage withdrawal |
| 14 | `final_container_bound` | Stage absent; stopped final returned ID and full physical configuration freshly proved and durably bound |
| 15 | `cutover_intent` | Exact stopped final, unchanged prepared claim/runtime heads, predecessor ownership and successor interface/bind proof; authorize predecessor stop then final start |
| 16 | `successor_serving` | Final config and every LAN/loopback route proved; exact predecessor stopped, no effective old Rig bind and stable joint topology |
| 17 | `handover_committed` | Fresh terminal physical proof; authorize only create-only commit receipt installation and readback |

From phases thirteen through sixteen, one next-sequence `rollback_intent`
may be followed only by `handover_rolled_back`; the maximum sequence is
eighteen. There are no loops or forward transitions after rollback intent.
No progress may follow either terminal phase. Before cutover, abort requires
the predecessor unchanged and complete successor absence. After cutover,
abort additionally requires the original address uniquely present and the
exact predecessor configuration, complete routes and roster physically proved.
An unavailable or ambiguous original address cannot be reported as a successful
rollback. Its absence never substitutes for exact predecessor shutdown proof.

Commit and abort use one purpose-bound, create-only terminal artifact per
operation, with an explicit outcome kind. The artifact binds its terminal
progress record, prepared claim sequence, immutable intent/predecessor,
successor identity, complete roster, route/config plan and bound resources,
fresh final observations and creation time. The complete sequence-eleven route
plan remains referenced through immutable contextual digests.

A terminal phase without its receipt is an interrupted installation that may
be completed only after fresh proof. A receipt without its exact terminal
phase, conflicting terminal outcomes, extra records, gaps or changed history
fails closed. Discovery retains the single intent and original predecessor;
it does not resolve future cross-store lineage.

## Failure and recovery acceptance

| Boundary | Required behavior |
| --- | --- |
| Intent installed, stage still running | Fresh proof before exact stage withdrawal |
| Stage absent, final absent | Reprove retained resources/config authorization and exact absence before creation |
| Create uncertain, final ID not durably bound | Retain the unknown resource and report unresolved; no name-based adoption or cleanup |
| Bound final stopped or an effect acknowledgment lost | Reinspect bound IDs; continue only from an explicitly recognized exact state |
| Predecessor stopped, successor cannot serve | Exact compensation only when original service can be physically restored; otherwise recovery-only |
| Receipt write may have installed | Stop this invocation; fresh history read and physical proof before exact replay |
| Protected terminal commit | Reattest/install the same receipt only; no rollback or conflicting abort |
| Ownership/history/topology uncertain | Preserve evidence and fences; no broader Docker mutation |

Recovery reacquires the complete lock chain and chooses from durable evidence.
No in-memory phase authorizes restart. All source grants, allocations, profiles,
predecessor journals and earlier progress remain immutable.

Forward and rollback are separate private entrypoints. A transient observation
or effect error leaves the operation pending; it does not select destructive
compensation. An explicit rollback invocation must freshly prove admission and
install `rollback_intent` before cleanup. Subsequent recovery follows that
durable direction, including when invoked through the forward entrypoint.

## Verification plan and current evidence

| Check | Evidence required | Current result |
| --- | --- | --- |
| Inherited baseline | Verified prior source and focused sequence-twelve replay | Prior full Go checkpoint at `7d83b583`, final 68-test group plus vet/build/tagged compilation at `0aa8d785`; base documentation commit `39b217f` retains that Go/workflow tree. New-worktree sequence-twelve copy/replay baseline passed in 15.082s, reported by the parent coordinator. |
| Final container validator | Exact bound identity or provisional stopped readback; hardening, mounts, LAN/loopback publications and stopped/running network proof | Two required top-level tests discovered and passed locally in 26.181s; production Docker inspection remains pending. |
| Builders and physical observer | Exact IDs/labels/image/config/mounts/binds/networks; malformed and foreign substitutions; bounded reads | Plan tests passed in 28.019s; four resource/withdrawal/projection tests passed in 27.655s; two direct host/application-proof tests passed in 13.343s. Full driver journey remains pending. |
| Coordinator and history | Every transition/effect/write boundary; unchanged SQL/source bytes; stale heads; forged/gapped/conflicting records; cancellation and release errors | 45 existing protected-history top-level tests passed in 51.263s; one symlink subtest skipped for unavailable Windows privilege. New coordinator fault tests remain in progress. |
| Restart and compensation | Fresh Manager and actual subprocess exit after predecessor stop; lost acknowledgments; unbound creation retained; both terminal outcomes | Pending |
| Hosted Docker | Two real test-owned host addresses; at least two applications including loopback-only; full LAN/loopback routes, expected 404 behavior, old-bind absence, rollback restoration and exact cleanup | Pending |
| Final integration | Full relevant Go checks, formatter/diff checks, docs, Linux race and independent security/integration review | Pending |

The initial contract/evidence documentation passed the following checks:

```text
pnpm --dir docs install --frozen-lockfile --offline
pnpm --dir docs check:workflow
pnpm --dir docs build
```

The install reused all 129 cached packages without downloads and finished in
1.9s; the workflow contract check passed and VitePress built in 3.06s.
An earlier sandbox invocation attempted automatic dependency installation,
could not access the package cache and was interrupted. That attempt is not a
successful verification result. The documentation diff check passed. These
results validate the initial documentation only, not the handover runtime.

After adding the focused container evidence and rollback-direction policy,
`pnpm --dir docs check:workflow` passed again and `pnpm --dir docs build`
completed in 2.92s. This remains documentation evidence only.

The focused final-container command was:

```powershell
$env:GOCACHE='C:/Users/huang/Documents/Projects/Rig/.go-cache-m3'
go test -mod=readonly -count=1 -timeout=4m ./internal/generatedingress -run '^TestGatewayRebindFinalHandoverContainer'
```

It passed in 26.181s with normal Windows filesystem access for the inherited
protected-history fixture. A separate `go test -mod=readonly
./internal/generatedingress -list '^TestGatewayRebindFinalHandoverContainer'`
check required both exact expected test names and passed. The tests accept
stopped and running exact identities, Docker's mount order, image metadata,
empty exposed-port entries and omitted stopped attachments. They reject an
unbound named container, provisional running identity, altered ownership,
security/resource settings, publications, or network/runtime evidence. These
results cover the pure validator; joint resource observation, actual effects
and handover acceptance require the remaining gates.

Additional working-tree checkpoints used normal Windows filesystem access and
the shared Go cache above:

```text
go test -mod=readonly -p=1 -count=1 -timeout=5m -run '^TestGatewayRebindFinalHandoverPlan' ./internal/generatedingress
go test -mod=readonly -p=1 -count=1 -timeout=5m -run '^TestGatewayRebindFinalHandover(Withdrawal|Driver|PredecessorProjection)' ./internal/generatedingress
go test -mod=readonly -p=1 -count=1 -timeout=5m -run '^TestGatewayRebindFinalHandover(ApplicationProof|HostProof)' ./internal/generatedingress
go test -mod=readonly -p=1 -json -count=1 -timeout=10m -run '^(TestGatewayRebindProgress|TestGatewayRebindProtectedIntent|TestGatewayHistory)' ./internal/generatedingress
```

The protected-history group first required nonempty discovery and found 45
top-level tests. Its JSON result has 45 top-level passes, zero failures and one
skipped `symlink_artifact` subtest because Windows denied symlink creation.
Linux still needs to establish that check. This is incremental evidence, not
a final full-suite or frozen-source claim.

The direct observer-helper tests reject unreadable candidate/route/Docker
inventories, missing or duplicate selected interfaces, unrelated routes and
network IDs/prefixes, extra or missing application members, replaced network
identity, changed ownership, stopped gateway attachments, stolen aliases and
unhealthy application endpoints. Resource tests reject changed volume creation
identity, mountpoints, labels, options, foreign users, truncated user censuses
and unexpected ingress members. Predecessor normalization retains unrelated
members and does not mutate its input.

Independent review identified that a failed TCP connection alone does not
prove a listener absent. The new handover-specific negative probe accepts only
an explicit connection refusal within a bounded TCP4 deadline on a canonical
private or loopback address. Timeout, permission failure, unreachable network,
cancellation, unknown error and connected socket are rejected. Tests cover
these cases and every withdrawn publication, including retained loopback.
The reviewer confirmed the fix; actual Docker publication proof remains open.

Earlier integration compile attempts did not run tests: one used the restricted
user's inaccessible default Go cache, and two encountered incomplete coordinated
source or a missing test import. The normal-environment reruns above passed.

### Newly observed prerequisite Docker failure

PR #135 remains a draft at `f6966a1512bbfa40468567655bb1fd9a1b3197e7`.
Its [stage-config-copy job](https://github.com/tyhuang9/rig/actions/runs/37359439273/job/111930044487)
failed both named live journeys before copy: direct path in 71.67s and
lost-acknowledgment path in 66.43s. Both reported that the sequence-six config
mount was not a strictly empty archive; the always-run cleanup passed.
This supersedes the earlier all-pending/no-failure snapshot for that gate.

Read-only inspection of the exact pinned image found an empty `config/caddy/`
directory in layer `sha256:ee31d5a470f0e64a85c28c82ad55a5bd6b5359db59197c76b9daba7f36c288e9`,
with UID/GID zero, mode `01777`, size zero and no link. The layer's downloaded
compressed bytes matched its SHA256. Docker's documented default is to
[populate an empty mounted volume from image contents](https://docs.docker.com/engine/storage/volumes/#mounting-a-volume-over-existing-data).
The existing strict parser rejects that directory. A bounded compatibility
correction is being prepared on the already published prerequisite branches;
unknown files, directory children, links and metadata must remain rejected.
The actual Docker-copy TAR root/header representation is not yet observed.

The local Docker CLI exists, but a read-only server-version request failed
because the `dockerDesktopLinuxEngine` pipe is absent. No local Docker runtime
acceptance is claimed. The two-address hosted journey remains required.

The live journey uses a new standalone fixture. Existing stage-start and
sequence-twelve callback assertions requiring an unchanged predecessor and no
forwarded application requests remain intact. Hosted address setup must be
bounded to a disposable runner, record its exact test-owned interfaces and
addresses, and clean up only those resources. A same-address test cannot stand
in for changed-address acceptance. Real second-device DHCP/interface-change
qualification remains a later LAN-08 acceptance gate.

Implementation, deterministic fault tests and disposable Docker fixtures may
progress locally while prerequisite hosted checks finish. Physical acceptance
claims require the exact corrected prerequisite heads and this branch's actual
Docker evidence. Compilation or skipped live tests are not physical acceptance.

### Reviewed source checkpoint and prerequisite correction

The strict archive correction was published additively to the existing draft
PRs #129–135. Each remote head, stacked base and draft status was checked after
publication. PR #135 is now `bd347cab711dd98f35a940062a4751b415209442`; its
36 downstream archive/start/final-config tests passed in 285.669s with no
failures or skips. The [recorded correction](./m3-rebind-final-config-intent-evidence.md#prerequisite-archive-integration-2026-10-05)
includes the earlier failed hosted job and remaining physical gates.

This branch incorporates those prerequisites by additive merges `8068ed8`
and `e8cf18d`, retaining the unpublished sequence-twelve base unchanged.
Its final-config pair parser now admits the same single exact empty image
directory. The new regression failed before correction in 0.756s, then all
empty/stage/final archive tests passed in 0.770s. The first test-fixture attempt
also used a regular-file TAR header with a trailing slash, which Go's archive
writer rejected; that fixture was corrected before the meaningful red run.
No directory children, ownership changes, metadata or alternate paths became
valid, and no existing config bytes or digests changed.

Independent source review found a pre-write admission gap: a structurally
self-consistent sequence-thirteen plan could fail full context validation
only after immutable installation. The writer now checks the actual protected
predecessor, source and retained sequence twelve before new writes and exact
replay. Recomputed configuration/port forgeries must leave no file and keep
the prior history readable. Stopped predecessor proof also requires its stop
digest to equal its physical observation digest. The reviewer rechecked both
fixes and the final archive alignment without finding another source blocker.

Working-tree test checkpoints, all with normal Windows protected-file access:

| Check | Actual result |
| --- | --- |
| Initial full coordinator group | Failed in 715.722s solely in subprocess recovery: the child reused a timestamp older than its latest durable record. All other selected coordinator cases passed. |
| Corrected process and rollback group | `go test -mod=readonly -p=1 -count=1 -timeout=8m -v -run '^TestGatewayRebindFinalHandover(ProcessCrashAfterPredecessorStopRecovers\|CrashHelper\|RollbackRestoresServiceBeforeTerminalAbort)$' ./internal/generatedingress` passed in 185.078s. |
| Six rollback effect acknowledgment-loss cases | `go test -mod=readonly -p=1 -count=1 -timeout=8m -run '^TestGatewayRebindFinalHandoverRollbackLostAcknowledgmentResumesWithoutRepeatingEffects$' ./internal/generatedingress` passed in 276.723s. Fresh normal resume retained rollback direction and did not repeat completed effects. |
| Progress and terminal validation | All five discovered `^TestGatewayRebindFinalHandover(Progress\|Terminal)` tests passed in 99.724s; the pre-write regression separately passed in 32.853s. |
| Final real subprocess regression | After stopped-digest alignment, the process-recovery selection passed in 30.878s. Both live Docker tests skipped without opt-in; the child guard returned without a live effect. |

The fixed subprocess fixture derives its next timestamp from actual durable
progress. It retains real SQLite and protected files across child process exit,
then verifies resume, terminal receipt and effect-free replay. The six rollback
cases establish acknowledgment uncertainty with fake physical effects; they do
not establish actual Docker behavior.

At committed source `c2d3daf`, all-package `go vet -mod=readonly ./...` and
`go build -mod=readonly -buildvcs=false ./...` passed. Tagged compile-only
`go test -mod=readonly -p=1 -tags live_docker -run '^$' ./cmd/hostd
./internal/generatedingress` passed in 0.718s and 0.725s respectively.
Formatting and aggregate diff checks passed before the source commit. Required
discovery found 25 deterministic handover top-level tests; the full Go run is
pending, using a larger local timeout for Windows protected-file fixture costs.

The two hosted jobs use separate disposable Linux runners, reject overlap with
both assigned prefixes and non-default host routes before creating their exact
owned dummy interface, and prove distinct old/successor addresses. Each builds
a LAN application and a loopback-only application, drives real sequence twelve,
exits the child process after an actual Docker effect, then recovers with a
fresh process. Commit and rollback assertions cover exact responses, unknown
Host 404, old listener withdrawal, immutable SQL/source/runtime heads/history,
and exact-bound cleanup. Their required named-pass and no-skip guards cannot be
replaced by compile-only success. Second-device LAN qualification remains open.

### Autosave compatibility discovered during integration

The pinned Caddy version [loads and canonicalizes the raw JSON before running
it](https://github.com/caddyserver/caddy/blob/v2.11.4/caddy.go#L192-L231), then
[writes its autosave with mode `0600`](https://github.com/caddyserver/caddy/blob/v2.11.4/caddy.go#L352-L375).
Rig's immutable configuration has not disabled persistence; its UID/GID
`1000:1000` process can write the image's `01777` config directory. Therefore
the existing strict stage and final inventory parsers would reject normal
post-start `caddy/autosave.json`. This is a source-confirmed integration risk;
actual Docker TAR evidence is still pending.

The bounded correction must retain strict pre-start inventory and admit only
an exact canonical snapshot after durable start authorization. Stage snapshots
remain legitimate after compensated stop and while active.json is inactive.
A stopped final after cutover intent may contain the old stage or new active
snapshot because start acknowledgment can be lost. A running final must prove
its active snapshot when present. Unknown paths, bytes, metadata, owners and
permissions remain invalid. Archive limits must accommodate the additional
bounded file without increasing generic Docker output limits.

The running full suite remains evidence for frozen `c2d3daf`, and cannot prove
this pending correction. The prior conditional source-review verdict is
superseded for publication readiness until this issue is fixed and reviewed.

At `c2d3daf`, `pnpm --dir web install --frozen-lockfile --offline` reused all
172 packages. `pnpm --dir web test` passed all 502 tests in 19 files (11.41s),
and `pnpm --dir web build` passed type checking and Vite production build
(1.44s). Vite reported the existing over-500KiB bundle warning; no web source
changed. Documentation workflow checks and build passed in 3.05s after the
initial evidence update. Generated outputs and dependencies remained ignored.

### Autosave correction verification (2026-10-05)

The source-confirmed gap above is corrected without rewriting prior generated
configs, protected records or digests. The PR #132 correction `8801a09` is
integrated additively, followed by its PR #135 descendant `e46185f`.
Existing drafts #132–135 were updated with their authorized correction;
this handover branch remains unpublished.

Final inventory is bounded to 192 KiB for two approved configs, one optional
snapshot and TAR framing. Each config remains bounded to 60 KiB. The default
sequence-eleven/twelve reader accepts only the exact canonical stage snapshot.
Only the handover reader selects the phase-specific policy: stage before
cutover, either stage or active for a stopped final under uncertain cutover,
and active for a running final or a proved successor. The actual rollback
coordinator passes its scanner-validated durable origin as invocation-local
context; no new rollback field is persisted. All original files remain byte
exact, and optional autosave requires the exact pinned directory, path,
regular-file type, UID/GID 1000 and mode 0600. Unknown entries, duplicates,
hidden metadata, changed bytes, invalid padding and trailing data are rejected.

| Check | Actual result |
| --- | --- |
| Default final-reader red/green | The legitimate stage-autosave regression failed before correction in 0.792s. It passes after correction, including exact stage-only and stage-plus-active inventories in different archive orders. |
| Parser, phase policy and production reader group | All six new top-level tests passed. The combined 12-test run failed in 243.334s only because one existing request assertion still expected 128 KiB; that assertion was corrected to 192 KiB. |
| Corrected existing bounded-read regression | `go test -mod=readonly -count=1 -run '^TestGatewayRebindFinalConfigCopyDriverInventoryUsesBoundedPinnedReadAndClearsResults$' ./internal/generatedingress` passed all nine subcases in 0.951s. Generic command limits remain unchanged. |
| Actual rollback-origin propagation | The new coordinator matrix passed in 228.04s across durable sequences 13, 14, 15 before/after uncertain start, and 16. It verifies origin on each rollback read, terminal replay and unchanged history/effects. |
| Production handover config-read policy | The new matrix passed in 14.48s, including rejection before Docker dispatch for invalid authority, phase-specific snapshot bytes, pinned read arguments, bounded output and buffer clearing. |
| Real progressed-history regression | `go test -mod=readonly -p=1 -count=1 -timeout=4m -run '^TestGatewayRebindFinalConfigAutosaveRetainsStartedStageProofAcrossProgress$' ./internal/generatedingress` passed in 17.775s. It uses real protected progress at sequences 10, 11 and 12, retaining the durable sequence-nine proof and requiring exact history readback. |
| Static and tagged compilation | Repository-wide `go vet -mod=readonly ./...` and `go build -mod=readonly -buildvcs=false ./...` passed on the integrated correction. `go test -mod=readonly -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed (1.176s and 0.854s); no live effects ran. |
| Independent final source review | GO for local correctness after checking the completed parser/policy/coordinator tests, immutable formats, phase authority and pinned Caddy source. No source blocker remained. No CodeRabbit result is claimed. |

The corrected final-config-copy prerequisite was published as draft PR #136
at `bdbb697` after separate explicit approval. Its head, base and open draft
status were verified and it was attached to the task. Merge `6dafa34` retains
that exact branch history in this candidate; comparison before/after the merge
found only documentation differences and no Go, module or workflow change.
The prerequisite's 76 explicitly discovered regression tests all passed in
537.126s without failures/skips, including the shared final archive parser and
the real sequence-ten/eleven/twelve history regression. This worktree's locked
offline docs install, workflow check and build also passed (3.25s).

The initial combined-test failure is retained above; the obsolete assertion's
isolated rerun is not represented as a second full group pass. The concurrent
full Go run on frozen `c2d3daf` is also separate from this correction. Actual
Docker TAR headers, autosave timing, cutover and process-recovery effects still
require the hosted jobs. These local results do not substitute for physical
Docker or second-device LAN acceptance.

### Completed frozen full-suite checkpoint

At source `c2d3daf5c5d8810005fadacccbda8958b4a0f3a5`, the following command
completed successfully using normal Windows protected-file access:

```text
go test -mod=readonly -p=1 -json -count=1 -timeout=60m ./...
```

JSON inspection confirmed 48 packages passed and 2,048 top-level tests passed,
with zero failures. All 25 explicitly discovered deterministic handover tests
passed exactly once. The ingress package took 2326.865s. The local timeout
accommodates the measured Windows protected-file fixture cost; it does not
alter hosted job limits. Two packages reported no test files. There were
53 test/subtest skips for live-service opt-ins, platform-specific permission
checks or unavailable Windows symlink privileges. These are not acceptance
passes; in particular both new handover Docker journeys skipped locally.

The full run stayed on its frozen source while the autosave correction was
implemented in a separate worktree. Only after completion is the correction
integrated into the handover branch. The current candidate's evidence is the
frozen full-suite checkpoint plus the exact incremental correction checks
above, not a claim that the full suite ran again after the correction. No Go,
module or workflow change occurred during the final documentation-only merges.

## Rollback and remaining gates

Keep this entrypoint private and unwired. Reverting code does not authorize
deleting uncertain Docker resources or protected history. Preterminal recovery
may compensate only exact-bound resources; a successful abort requires the
proofs above. Terminal commit recovery preserves its decision and receipt.

Later work supplies the guarded SQL transfer, distinct effective-profile and
lineage resolver, recovery-aware readers/startup, narrow durable fence release,
administrator journey and physical second-device acceptance. No managed
database, Neon provisioning, or unrelated infrastructure is included.
