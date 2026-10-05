# M3 LAN gateway rebind cutover contract

Status: design contract only. This document defines the minimum safe contract
for LAN-08. Migration 034 is a dormant ledger foundation; it does not provide
a rebind writer, Docker cutover, recovery endpoint, URL publication, or fence
release. The guarded cutover migration is reserved for migration 035 or later.

## Existing boundary

The contract extends the following behavior without weakening it:

- Migration 026 retains every gateway upgrade claim and event, pins the exact
  profile for every non-rolled-back claim, and permits only its declared state
  transitions (`internal/database/migrations/026_lan_gateway_upgrade_claims.sql`).
- Migration 033 stores one immutable `prepared` rebind claim, roster rows,
  approval identities, and the committed predecessor identity. Its triggers
  validate each inserted roster row and administrator role; the startup
  read-back validates full roster completeness and canonical approval digests.
  Its broad SQL fence deliberately has no writer or release path
  (`internal/database/migrations/033_lan_gateway_rebind_lineage.sql`).
- `GatewayRebindStartupSnapshot` validates the claim, approvals, current
  predecessor, committed predecessor upgrade, complete roster, committed
  grants, and current runtime heads in one SQLite snapshot
  (`internal/appaccess/gateway_rebind_startup_snapshot.go`).
- `InspectGatewayRebindPredecessor` compares that snapshot twice with the same
  committed protected v2 state, journal, LAN bindings, and protected history
  fingerprints while the gateway locks are held. It is read-only and does not
  prove Docker topology (`internal/generatedingress/gateway_rebind_predecessor.go`).
- Deployment effects are serialized by the private per-data-root OS lease in
  `internal/runtime/deploymenteffects/lock.go`. Production startup and workers
  hold that lease around recovery and deployment effects. The standalone
  quiescence census is informational; the claim writer must repeat it in its
  own write transaction (`internal/appaccess/gateway_rebind_quiescence.go`).
- The existing v2 protected history uses purpose-bound state, journal, abort,
  and rollback-retirement artifacts. A generation records immutable Docker
  resource identities, and the history scanner rejects gaps, duplicates, and
  changed artifacts (`internal/generatedingress/upgrade_state.go` and
  `internal/generatedingress/upgrade_history.go`).
- Normal generated-ingress operations use the Manager mutex, then the gateway
  OS lock, then a fresh SQLite fence check. The raw gateway lock is reserved
  for exact-owned emergency stop and the read-only predecessor attestor
  (`internal/generatedingress/manager_lock.go`).
- Host preflight requires an exact approved interface/address and a stable,
  complete host-route and Docker-network inventory. It never silently chooses
  another interface (`internal/hostnetwork/selection.go`,
  `internal/generatedingress/gateway_v2_network_plan.go`, and
  `internal/generatedingress/gateway_v2_host_preflight.go`).
- The current protected-route validator requires every LAN binding's profile
  to equal the route state's profile. Current grant and disable authorization
  likewise require the immutable allocation/grant profile to equal the current
  profile head (`internal/generatedingress/upgrade_state.go`,
  `internal/appaccess/grant_claim.go`, and
  `internal/appaccess/disable_authorization.go`). A rebind transfer cannot be
  released until transfer-aware replacements preserve those checks.
- The current v2 identity uses fixed final-container, config-volume,
  data-volume, and ingress-network names; only its stage name is
  operation-scoped (`internal/generatedingress/upgrade_state.go`). Those fixed
  names belong to the initial v2 predecessor and cannot identify a retained
  predecessor and a rebind successor at the same time.

These are invariants, not conveniences. The guarded migration and cutover writer
must fail closed if any of them cannot be re-established from durable state.

## Required lock and transaction order

Every claim creation, cutover, rollback, recovery, and fence-release attempt
must acquire and retain locks in this order:

1. deployment effects lease;
2. generated-ingress Manager mutex;
3. cross-process gateway OS lock;
4. SQLite transaction, only while a database mutation or snapshot is needed.

No path may acquire an earlier lock while holding a later lock. Startup uses
the same order before it chooses normal versus recovery-only operation.

No SQLite write transaction may remain open across a Docker command,
host-network observation, HTTP probe, or physical-device proof. A read
transaction may surround only read-only attestation and must close before the
first external effect. The effect must be preceded by protected intent and
followed by a new SQLite transaction that validates the exact expected head
before recording a state transition. This prevents a long SQLite lock from
becoming an external-effect lease and keeps crash recovery dependent on
durable evidence rather than an open connection.

Within one active attempt, the writer must hold the deployment effects lease
from its first authorization observation through the next durable terminal or
recovery-only boundary. It may release the locks while awaiting an
operator-directed recovery or later retry only after the active SQL fence and
exact recovery evidence are durable. Every resumed attempt reacquires the full
lock chain. Releasing the lease is not evidence that work is quiescent.

## Admission and prepared-claim creation

The following sequence is the only route into `prepared`:

1. Validate both purpose-bound administrator approvals and canonical request
   digests. The rebind approval covers the predecessor protected identity,
   successor identity, and roster. The existing configure approval separately
   covers the successor profile.
2. Acquire the locks in the required order. Run a dedicated **preclaim**
   attestor against the approved request and proposed roster. With the gateway
   locks held, it must prove there is no active rebind claim and compare two
   stable reads of the current SQLite profile/allocation/grant/runtime heads,
   protected history, and Docker predecessor. It returns evidence for claim
   insertion but cannot authorize an effect.
3. Prove the exact predecessor Docker ownership from the committed journal:
   final container ID and complete ownership labels, image ID, ingress network
   ID, config/data volume identities, route configuration, and every roster LAN
   binding. Extra owned resources, a pending route, a LAN recovery batch, or an
   unstable inventory rejects admission.
4. Preflight the approved **successor** interface/address and its complete host
   and Docker network view. Select or validate a non-conflicting successor
   internal network plan and retain its canonical digest and observation for
   the post-claim protected intent. Do not persist successor protected intent
   before the claim commits, and revalidate the plan before its first effect.
5. Start one immediate SQLite write transaction. Inside that same transaction,
   repeat the complete predecessor, approvals, current heads, roster, and
   quiescence checks. Every job and deployment must have a recognized terminal
   status. Insert the claim, its initial event, and the exact roster, then read
   them back before commit.
6. After commit and before writing successor intent or causing any external
   effect, run the prepared-claim attestor again. This pass must use the exact
   inserted claim/roster and compare it with protected history and exact Docker
   ownership under the same gateway locks.

The existing `InspectGatewayRebindPredecessor` is the post-insert protected
history half of step 6: it deliberately requires exactly one prepared claim.
It is not the preclaim attestor for step 2, and it does not prove Docker
topology. Its public method acquires the Manager and gateway locks, so a writer
already holding them must extract and call a locked attestation core instead
of invoking the public method recursively. The implementation needs a separate
preclaim API and must combine
the post-insert protected comparison with a fresh exact Docker ownership proof
before any successor artifact or Docker effect. A preclaim proof must never be
reused after claim insertion.

A prior read-only census cannot authorize step 5. The repeated census inside
the claim-insert transaction is the decision that establishes quiescence.
The immediate transaction serializes concurrent SQLite writers: a job or
deployment row committed first is included in the census, while a writer that
starts afterward observes the committed rebind fence before it can dispatch an
effect. The effects lease separately prevents an already-admitted worker or
startup recovery from crossing the census/claim boundary. The guarded
migration and production admission tests must prove that combined invariant
for every path that can start a deployment effect; this document does not
assume every job enqueue itself already acquires the lease.

### Old interface and address rule

The predecessor interface or address may be missing because a NIC disappeared
or DHCP changed. That condition does not invalidate the approved rebind by
itself. Admission must still prove the exact protected predecessor and exact
Docker ownership, but it must not call the predecessor selected-interface
preflight as a prerequisite for rebind. It must never substitute a different
observed address for the recorded predecessor.

An absent predecessor address means only that the old address is unavailable.
Rig must not claim that the old URL is serving, returns 404, has been reached,
or has been physically withdrawn. The operator surface and evidence must label
the old URL as unverified/unavailable. Only the successor URL may become the
published current URL after the final acceptance gates below.

## Durable successor format

The successor must be a new purpose-bound protected history generation. It
must not overwrite, rename, delete, reinterpret, or reuse the predecessor
state, journal, receipt, or resource identities. The existing predecessor
generation remains valid immutable evidence after commit and after rollback.

The new generation must contain these exact bindings, either directly or by a
canonical digest plus a strict read-back of the referenced protected object:

| Binding | Required protected evidence |
| --- | --- |
| Rebind claim | rebind operation ID, claim request digest, claim spec digest, rebind approver, configure approver, and both approval action digests |
| Predecessor | protected generation, typed source reference (committed migration-026 upgrade operation or prior committed rebind operation and terminal receipt), profile revision ID/number/spec digest, protected state digest, journal digest, identity digest, and complete immutable Docker resource bindings |
| Successor profile | profile revision ID/number, configure operation ID, request digest, selected IPv4, interface ID, port range, profile spec digest, and approved actor |
| Roster | roster digest/count and ordered per-entry digests; each entry retains allocation, access revision, committed grant, serving deployment/release/slot, route generation, and protected LAN binding |
| Network | canonical successor internal network plan and plan digest derived from the stable preflight inventory |
| Successor identity | identity-format version and deterministic identity digest scoped to the rebind operation and successor profile |
| Docker resources | pinned image ID, ingress network ID, config/data volume identity digests, and stage/final container IDs, each bound before the next mutation that uses it |
| Progress | monotonically sequenced phase records with previous-record digest, phase, observed topology digest, and time; records are create-only |
| Terminal result | one create-only commit, rollback, or abort receipt bound to the final phase record and all fields above |

Every rebind successor uses generation-scoped Docker names derived from the
protected history generation and rebind operation ID. For generation `N > 0`,
with `N` rendered as 20 decimal digits and `OP` as the canonical operation UUID,
the required names are:

| Resource | Deterministic name |
| --- | --- |
| Final container | `rig-generated-caddy-v2-gNNNNNNNNNNNNNNNNNNNN-OP` |
| Stage container | `rig-generated-caddy-v2-stage-gNNNNNNNNNNNNNNNNNNNN-OP` |
| Config volume | `rig-generated-caddy-config-v2-gNNNNNNNNNNNNNNNNNNNN-OP` |
| Data volume | `rig-generated-caddy-data-v2-gNNNNNNNNNNNNNNNNNNNN-OP` |
| Ingress network | `rig-generated-caddy-ingress-v2-gNNNNNNNNNNNNNNNNNNNN-OP` |

The successor identity format is `v2-rebind-1`; the existing `v2` format stays
reserved for fixed-name upgrade identities. Its canonical identity digest
includes the format, history generation, operation ID, pinned image digest,
all five Docker names, both container hostnames, and the stage/active config
filenames. The final and stage Docker names exceed Linux's hostname limit, so
their hostnames are distinct short values. For role `f` or `s`, derive
`rig-v2r-ROLE-` followed by the first 32 lowercase hex digits of SHA-256 over
the canonical tuple `(v2-rebind-1, generation, operation ID, role)`. These
hostnames are 42 bytes and must be validated against the protected
identity; a hostname or truncated digest alone never proves Docker ownership.
A `v2` identity
cannot be accepted as a rebind successor, and a `v2-rebind-1` identity cannot
be used to reinterpret a predecessor.

The initial predecessor established by the v1-to-v2 upgrade keeps its existing
fixed names. After one rebind, the next predecessor instead keeps the exact
generation-scoped names from that prior committed `v2-rebind-1` identity and
journal. Neither kind of predecessor is renamed or reused by a successor. The
successor history
generation, operation ID, all five names, and their canonical identity digest
are written in protected intent before resource creation. Every observer,
create/start/stop/remove mutator, recovery decision, and emergency stop must
load and validate the names from that exact protected identity. It must not
reconstruct a target from the current fixed-name constants, select the newest
name, or act on a matching prefix alone. Retaining a stopped predecessor
therefore cannot collide with creation of this or a later successor.

The successor route state must contain the complete application map, not just
the LAN subset. For every roster entry, the source LAN binding must retain the
exact committed grant attempt, request digest, allocation, owner operation,
access revision, port, approver, and predecessor profile fields. Those fields
are grant proof and must not be rewritten to pretend the old grant approved a
new profile. A separate rebind-transfer binding must name the rebind claim,
source binding digest, successor profile, and corresponding append-only SQLite
transfer entry. The effective successor route is valid only when both bindings
match. The route itself must remain bound to the roster's serving deployment,
release, slot, and route generation; a matching port with a changed runtime
head is stale.

Transfer-aware validation is part of the successor format, not follow-up
cleanup. Before fence release:

- `validGatewayV2RouteState`/`validGatewayV2AppRoute` must validate either a
  native binding to the current profile or an exact source binding plus the
  protected/SQLite rebind-transfer binding. The access digest is recomputed
  against its immutable source profile; the effective port is also checked
  against the successor profile range.
- Grant authorization must accept an old immutable allocation/profile only
  through the unique committed transfer whose source binding, current profile
  head, roster entry, and receipt all match. A new native allocation under the
  successor continues to use the ordinary current-head rules.
- Disable authorization must resolve the same transfer for a nonterminal
  disable and must retain the original grant/allocation identity for terminal
  history. It may not treat a changed profile head alone as proof of transfer.
- Startup snapshots and generated-ingress grant/disable startup attestors must
  use the same resolver, reject missing/duplicate/gapped transfers, and derive
  one effective current binding without rewriting the source grant.
- A later rebind must link each roster entry to the preceding effective
  transfer digest. Its validator must prove an unbroken lineage back to the
  immutable source allocation/grant and must not accept an arbitrary stale
  profile as current.

Until all of these read and authorization paths are transfer-aware, the claim
must remain active and normal startup, grant, disable, and later-rebind actions
remain fenced.

The shared resolver must return two deliberately separate projections: the
raw grant/allocation/profile proof exactly as retained, and an effective
profile projection containing the current successor profile, terminal transfer
chain digest, and receipt digest. `ProfileHeadCurrent` in
`internal/appaccess/grant_startup_snapshot.go` must be computed from that
effective projection after a rebind. The comparisons in
`validateCurrentAppAccessGrantSpec`, `readAppAccessDisableAuthorization`, and
`gatewayV2LANDisableStateMatches` must likewise use the validated effective
profile for current authorization while continuing to compare the raw request
and grant proof with their original allocation/profile. No caller may replace
the raw profile fields with the effective values before digest validation
(`internal/appaccess/grant_claim.go`,
`internal/appaccess/disable_authorization.go`, and
`internal/generatedingress/gateway_v2_lan_disable.go`).

Protected progress is append-only. A phase advance creates the next sequenced
record and reads it back; it never edits an earlier record. Resource identity
fields are monotonic: an empty field may become the one observed identity in
the next record, and a non-empty field may only replay exactly. History
discovery must reject missing sequence numbers, duplicate sequences, multiple
terminal receipts, unknown files in the reserved history namespace, purpose
mismatches, or any changed retained artifact.

The private protected-history format defines two pre-effect records,
`successor_intent` and `stage_intent`, followed by create-only bindings for the
successor ingress network at sequence three, config volume at sequence four,
data volume at sequence five, and stopped stage container at sequence six.
Each later record preserves the exact prior stage payload and adds only the
observed identity of its own resource. The `stage_intent` record stores the
approved image content digest and separately observed Docker image ID.
Its storage validator binds the approved digest, successor identity, and
selected network plan to the installed intent; it checks the observed image
ID and topology digest only for syntax. The guarded network writer compares
both with fresh observations under the deployment lease and gateway locks
before creating a resource. Sequence three binds Docker's returned network ID
and the exact ownership digest. Sequences four and five bind the respective
Docker volume name, mountpoint, creation time, and ownership digest only after
all previously bound resources and the full prepared claim have been
reattested. Sequence six binds only the exact stopped stage-container identity
and immutable ownership/configuration digest after the same guarded proof of
the network, both volumes, pinned image, host topology, Docker census, and
prepared claim. It does not copy config, start the container, attach an
effective host listener, change the route, or advance SQLite. An effect whose
identity was not durably bound remains fenced for separately reviewed recovery;
its resource name alone never permits adoption or cleanup.
No serving container, terminal phase, or receipt is installed by this format.

The next protected record, sequence seven, is a create-only stage-config
intent. It must pin deterministic probe-and-404-only successor Caddy bytes by
raw content digest and length, the exact `/config/stage.json` destination, the
sequence-six stopped container and config-volume bindings, and the prepared
claim's protected predecessor. A guarded writer must reattest the exact
sequence-six physical state and prove the stopped container's config directory
empty from a complete, bounded Docker archive before recording this intent.
An unreadable or ambiguous directory is not evidence of emptiness. Sequence
seven authorizes no Docker copy, container start, public listener, route
change, or SQLite write;
the subsequent copy and its readback require a separate guarded effect and
receipt. The sequence-seven config bytes and probe token are an immutable v1
format. A future format change must preserve v1 regeneration for existing
history and introduce an explicit version before writing new records.

The implementation may reuse the existing generation filename conventions
only if the scanner can distinguish an ordinary v1-to-v2 upgrade from a
rebind-successor generation and validate the predecessor link. Treating a
committed predecessor as rolled back or retired merely to make the current
scanner accept another generation is prohibited.

## Cross-store state machine

The SQLite claim state and protected phase are separate durable records. A
transition is valid only when fresh read-back proves the required record on
both sides. The minimum state machine is:

| SQLite claim state | Required protected/Docker evidence | Allowed next state |
| --- | --- | --- |
| `prepared` | exact predecessor; successor intent is either absent or create-only and exact; no unbound Docker effect | `successor_ready`, `rolled_back`, or `unresolved` |
| `successor_ready` | successor terminal commit receipt; exact successor is serving; the receipt-bound exact predecessor final container is stopped and no effective old bind remains; SQLite still names predecessor as current | `database_committed` or `unresolved` |
| `database_committed` | successor profile/head and append-only roster transfers committed under the exact receipt; SQL fence remains active | `committed` or `unresolved` |
| `committed` | receipt re-read plus fresh exact host/Docker/config/history attestation; terminal and releases only the narrow rebind fence | none |
| `rolled_back` | terminal abort/rollback receipt; no successor publication or resource remains; when cutover had begun, exact predecessor service is physically proved | none |
| `unresolved` | any ambiguous, corrupt, unstable, partially installed, or unprovable state | before any `database_committed` event: exact recovery may move to `successor_ready`, `database_committed`, or `rolled_back`; after any `database_committed` event: exact recovery is roll-forward-only to `database_committed` or `committed` |

Every SQLite transition increments `state_sequence` by one and appends one
immutable event. The transition trigger must require the exact previous state
and sequence. `committed` and `rolled_back` are immutable terminal history;
`unresolved` remains fenced and is never treated as absence.

Migration 033 also declares `UNIQUE(operation_id, state)` on events. Recovery
can revisit `successor_ready` or `database_committed`, so migration 034 must
replace that uniqueness with sequence-keyed append-only events while retaining
`(operation_id, sequence)` uniqueness and immutable update/delete guards.
The later guarded transition writer must reject a replay with the wrong
preceding state or sequence.

Immutable event history, rather than only the current claim row, controls the
rollback boundary. If any event for the operation has state
`database_committed`, no later `unresolved` row may transition to
`rolled_back`, `prepared`, or `successor_ready`. The guarded migration must
enforce that rule in SQL with an exact `NOT EXISTS` predicate over retained
events for every rollback transition and with a positive prior-event predicate
for every roll-forward replay. Application code cannot override it.

The protected phases under `prepared` are, in order:

1. `successor_intent`: complete claim/profile/roster/network binding exists and
   no successor resource identity is claimed;
2. `stage_intent`: intent to create or reuse only the exact bound successor
   infrastructure;
3. `staged`: exact stage container serves the successor config while the
   predecessor is not mutated;
4. `cutover_intent`: durable authorization to start/promote the exact successor
   final container and then stop only the receipt-bound exact predecessor final
   container;
5. `successor_serving`: exact successor final container and every roster route
   are proved on the approved successor address; the exact predecessor final
   container is proved stopped and stable inventory shows no effective old Rig
   bind for the predecessor address/port range;
6. `committed`: exact resource identities and topology are terminal and a
   create-only commit receipt may be installed.

Before `cutover_intent`, compensation removes only exact-owned successor
resources and leaves the predecessor untouched. After `cutover_intent`, an
automatic rollback may be called successful only when the original interface
and address are again uniquely present and the exact predecessor container,
config, routes, and roster are proved serving. The rollback receipt must bind
that exact live host/Docker/config proof and prove complete successor absence.

If the old address is absent, changed, ambiguous, or cannot be probed after
cutover began, automatic rollback is not available. The operation moves to
`unresolved`; startup exposes only the exact recovery operation. Recovery may
finish the successor using its recorded intent or roll back after the original
address becomes provable. It must not label an unavailable predecessor as a
successful rollback.

Once SQLite reaches `database_committed`, recovery is roll-forward only. The
immutable successor profile and transfer history must not be undone by moving
the profile head backward or rewriting predecessor rows.

An absent old NIC does not make predecessor shutdown optional. Before the
successor commit receipt or `successor_ready`, the cutover must load the
predecessor final-container ID and complete ownership labels from the exact
claim-bound protected predecessor journal, stop only that container, and prove
from two stable inventories that it is stopped/not restarting and that no
effective old Rig bind remains. The resulting stop/topology digest is part of
the successor receipt. If ownership or stop cannot be proved, the operation is
`unresolved` and the successor is not committed.

Startup and every recovery replay must recheck that predecessor stop before it
accepts `successor_ready`, `database_committed`, or `committed`. If the old NIC
later returns and the predecessor final container is running, restarting, or
again exposes the old binding, startup must use only the same exact-owned
journal-bound stop, keep the process recovery gate closed, and re-attest both
sides. The terminal SQL claim remains immutable. Startup must not start normal
workers or publish the successor as current until the old effective bind is
absent again. This prevents an unavailable old endpoint from silently reviving
after a NIC or Docker restart while keeping unrelated containers outside the
mutation scope.

## SQLite ledger foundation and guarded commit

Migration 034 broadens the claim and event schema for retained terminal
history and creates inert command and transfer ledgers. It retains all 033
claim, event, and roster rows, while preserving migration 026's committed
upgrade claim, events, audit history, predecessor profile revision, and
predecessor protected generation unchanged. It keeps prepared-only insertion,
immutable claim state, all `EXISTS(any claim)` fences, and the existing profile
pins. No 034 ledger row can authorize a transition or effect. A later guarded
migration must replace those restrictions with the state machine above only
after receipt-bound writers and transfer-aware readers are ready.

Migration 026's active `lan_gateway_profile_upgrade_claim_pin_insert` and
`lan_gateway_profile_upgrade_claim_pin_head` triggers still reject a new
profile and head advance while its committed upgrade claim exists. The
guarded migration must replace those pins only with exact, purpose-bound
rebind exceptions for the approved successor profile and one receipt-bound
head transition; normal profile mutations remain pinned. The immutable
migration-026 claim,
events, and audit rows are retained without reinterpretation.

Migration 033's unconditional singleton is a dormant safety device, not the
long-term history model. Migration 034 must make its schema capable of
retaining multiple terminal claims while enforcing at most one nonterminal
claim across `prepared`, `successor_ready`, `database_committed`, and
`unresolved`. After a committed rebind, its successor profile and protected
generation become the exact
predecessor of the next deliberate rebind. A rolled-back claim leaves its
predecessor current but remains retained history.

The next-rebind lineage must distinguish an original predecessor established
by a committed migration-026 upgrade from one established by a prior committed
rebind. It must reference the real source claim and receipt in either case. It
must not manufacture a `lan_gateway_upgrade_claims` row for a rebind-created
profile or reinterpret `upgrade_generated_ingress` approval as rebind approval.
Migration 034 must represent this predecessor-lineage variant without making
the prior-rebind path writable until a genuine committed receipt exists.

The later guarded migration's snapshot reader must therefore be phase-aware.
The current `GatewayRebindStartupSnapshot` accepts zero claims and, when a
migration-033 claim exists, validates its current predecessor, absent
successor, approvals,
and complete roster through the read path. `InspectGatewayRebindPredecessor`
then requires exactly one prepared claim. Neither can be reused unchanged after
the successor profile exists or when retained terminal claims accumulate. The
new reader must validate every retained terminal lineage, select at most one
active claim, and apply the expected profile-head/receipt/transfer predicates
for that active claim's exact state.

The guarded migration needs narrow, purpose-bound exceptions for its writer:

- insert exactly the claim's already-approved successor profile revision;
- advance the singleton profile head exactly once from the recorded
  predecessor revision to the recorded successor revision;
- append exact allocation/profile transfer rows for every roster entry;
- record the receipt-bound claim transitions and events; and
- release only the runtime/profile/allocation fence owned by that exact claim
  after the transition to terminal `committed` or a fully proved
  `rolled_back`.

The exception predicates must bind the operation ID, expected prior head,
successor IDs and digests, roster digest/count, terminal receipt digest, and
current claim state/sequence. They must reject direct profile insertion, a
different next revision, partial roster transfer, changed runtime heads, and
replay under another claim. A generic "rebind active" bypass, disabling
foreign keys/triggers, or connection-global bypass flag is outside this
contract.

The rollback transition predicate must also prove that the immutable event
table contains no `database_committed` event for the operation. A roll-forward
replay from `unresolved` after database commit must prove that such an event
does exist and that its sequence precedes the current event. This predicate is
part of the guarded migration trigger, not only repository validation.

Existing allocations, access revisions, grants, upgrade claims, and profile
revisions remain immutable. Migration 034 adds an inert append-only transfer
ledger shaped to map each predecessor allocation/grant binding to the successor
profile; it must not rewrite historical rows or permit planted transfer rows.
The guarded writer and its SQL authorization mechanism require separate review.

## Commit receipt, read-back, and fence release

The create-only successor commit receipt must include:

- protected generation and terminal phase-record digest;
- rebind claim request/spec digest and current SQLite state sequence;
- predecessor and successor profile, identity, state, and journal digests;
- roster digest/count and ordered entry digests;
- every bound Docker resource identity;
- exact successor topology/config digest and successor URL;
- the exact predecessor final-container stop digest, ownership/journal digest,
  restart/running observation, and stable Docker bind inventory;
- predecessor address observation classified as present, absent, or ambiguous;
  when present, a negative listener proof for the complete old Rig port range;
  absence does not infer reachability or replace the exact-owned stop proof;
  and
- exact local host-interface, bind, Docker topology, config, route, and history
  observation digests plus their creation time.

After installing the receipt, the controller must close any SQLite transaction,
read the receipt again through its exact protected purpose, rescan stable
history, reinspect exact Docker ownership/topology, and compare all values with
the pending claim. Only then may one short SQLite transaction commit the
successor profile/head, complete append-only transfers, the
`database_committed` claim transition, and its immutable event atomically. A
partial commit of those records is prohibited: the event is the durable proof
that rollback is no longer allowed.

The SQL fence must remain active after that database commit. The controller
must make a fresh, receipt-bound local attestation of the exact successor
interface/address, actual host bind, final container ownership, Caddy config,
complete roster routes, stable Docker inventory, protected history, and
current SQLite snapshot. The same attestation must recheck that the exact
predecessor final container is stopped/not restarting and that no effective old
Rig bind exists; if the old IP is present, it must include a negative listener
proof over the old profile's complete port range. It must recheck the successor
interface after the route probe and reject an observation from before the
receipt or for another generation. A port probe alone is insufficient.

Only the final short SQLite transaction may change `database_committed` to
`committed`. It must re-read the receipt digest, fresh local-attestation digest,
current profile head, complete transfer roster, and claim sequence. That
transition is the narrow durable fence release. A read or verification failure
before commit leaves the claim active and startup recovery-only.

An OS-lock or deployment-effects-lease release error after the SQLite
`committed` transition cannot change terminal history back to active,
`unresolved`, or `rolled_back`. The current process must latch fail-stop: it
must not start normal workers, expose a normal controller result, or publish
the successor URL, and it must exit after bounded exact-owned safety handling.
A fresh process must reacquire the complete lock chain and re-attest the
terminal claim, receipt, transfer-aware stores, predecessor stop/no-old-bind
proof, successor topology, and immutable history before normal serving. The
failed process must not attempt a compensating SQL transition.

## Crash and failure boundaries

Recovery always reacquires the full lock chain and chooses from durable
evidence. It does not continue from an in-memory phase.

- Before claim commit: no rebind exists; exact create-only artifacts, if any,
  are unauthorized drift and block normal startup.
- After claim commit but before protected successor intent: the fence remains
  active; an exact no-effect abort receipt may authorize `rolled_back`.
- After intent but before any Docker effect: exact replay may continue, or an
  abort receipt proving all successor resources absent may roll back.
- After any successor resource creation: recover only identities already bound
  in protected progress. Unknown or extra resources move to `unresolved`.
- After `cutover_intent`: inspect both exact predecessor and successor
  topologies. Do not infer which serves from container names or port numbers.
- After successor receipt but before SQLite successor commit: read back the
  receipt and either replay the exact database commit or remain recovery-only.
- After SQLite successor commit but before fresh local attestation/fence
  release: keep the successor head and transfers, keep the fence, and recover
  forward.
- After local attestation but before final SQLite transition: revalidate the
  observation and exact receipt, then replay only the final transition.
- After terminal commit: startup accepts the successor only when SQLite,
  protected history, Docker, and the retained receipt agree exactly.

At any boundary, cancellation or timeout stops forward progress but does not
erase intent. A failed protected write that may have installed is treated as
installed-until-re-read. Before terminal SQLite commit, a failed OS-lock or
effects-lease release is unresolved. After terminal commit, it triggers the
process fail-stop latch described above without changing immutable SQL state.

When a database/protected comparison or route proof fails, the fail-closed
action may stop only a final container whose ID and complete ownership labels
match the latest valid purpose-bound journal. This is the existing
exact-owned emergency-stop rule in
`internal/generatedingress/gateway_v2_lan_startup.go`. It must not stop by
name, image label alone, port, or guessed successor identity. If exact ownership
cannot be proved, report unresolved and make no broader Docker mutation.

## Acceptance gates

Implementation is not ready to publish a successor URL or release the fence
until all of these have recorded evidence:

1. Migration tests from populated 033 databases prove retention, trigger
   replacement, exact purpose-bound exceptions, immutable predecessor history,
   complete transfer rosters, direct-SQL rejection, and downgrade/rollback
   limitations. They also prove two sequential successful rebinds, a terminal
   rollback followed by a later rebind, multiple retained terminal claims, and
   rejection of two simultaneous nonterminal claims. A claim that reaches
   `database_committed`, then `unresolved`, must be rejected by SQL if recovery
   attempts `rolled_back`; exact roll-forward replay must remain possible.
2. Unit and integration tests cover every state transition, invalid transition,
   stale sequence, digest mismatch, duplicate/missing history record, changed
   resource identity, unstable history, and release failure. Preclaim tests
   prove zero-claim attestation from the approved request; post-insert tests
   prove exact prepared-claim/protected/Docker reattestation and reject a stale
   preclaim result before any effect.
3. A cross-process concurrency test proves the lock order and proves that job
   assignment, startup recovery, gateway mutation, and claim creation cannot
   overlap. The claim-insert transaction test must create nonterminal work from
   a second handle at the boundary and fail closed.
4. A crash matrix terminates the controller after every durable write and every
   Docker effect, then proves deterministic replay, exact compensation, or
   recovery-only behavior without deleting history.
5. Hosted Linux Docker tests exercise successful rebind, bind collision,
   unstable network inventory, extra/foreign resources, exact-owned emergency
   stop, failed receipt install/read-back, failure before and after predecessor
   stop, and restart at every protected phase. They prove that all five
   successor Docker names are generation-scoped, that two sequential rebinds
   cannot collide with retained predecessor names, and that observers/mutators
   reject fixed-name, prefix-only, and wrong-generation substitutions.
   Race-enabled Go tests must pass.
6. LAN-08 physical acceptance uses a real second device to open the displayed
   successor URL and exercise the required application path. It covers
   same-NIC DHCP drift and a removed/disabled old NIC, confirms that the new URL
   serves every roster app, and confirms that Rig makes no serving/404 claim
   for an absent old address. This qualifies the implementation and platform;
   it is not a per-rebind runtime prerequisite for fence release.
7. Startup tests prove that a prepared, successor-ready, database-committed, or
   unresolved claim exposes only the pinned recovery operation, while terminal
   commit starts normal workers only after exact cross-store reattestation.
   Transfer tests cover restart projection, a new native grant after rebind,
   disable of a transferred old grant, and a later rebind, while proving the
   raw grant digest/profile remains unchanged. Restart tests also return the old
   NIC after commit and require an exact-owned predecessor stop plus negative
   old-listener proof before normal serving.
8. Fault tests inject OS-lock and deployment-effects-lease release errors both
   before and after terminal SQL commit. The preterminal case remains active;
   the postterminal case preserves `committed`, latches the process fail-stop,
   exposes no normal URL, and requires fresh-process reacquisition and terminal
   reattestation.
9. The final evidence records exact commands, commits, hosted CI runs, Docker
   observations, physical-device steps/results, remaining limitations, and
   rollback or recovery instructions.

## Explicit non-goals

This contract does not add managed databases, Neon provisioning, wildcard host
bindings, automatic interface selection, DNS changes, firewall changes,
router configuration, remote deployment, merge automation, or background URL
publication. It does not authorize changing application-owned external
database configuration or exposing scoped runtime secrets.

It also does not define a general migration engine for protected history, a
generic SQL trigger bypass, or cleanup of retained predecessor artifacts.
Resource retirement, if later required, needs its own exact-proof receipt and
must retain the claim, profile, roster, phase, and receipt history.

## Unresolved implementation choices

The following choices must be resolved before the guarded migration and
cutover writer are approved:

- the exact SQL tables and trigger mechanism for purpose-bound writer
  authorization and append-only allocation/profile transfers;
- the protected artifact filenames, purpose strings, and versions for phases
  after the two defined pre-effect records, plus their terminal receipt and
  recovery semantics;
- the canonical local topology/config observation digest and whether a future
  optional remote-device verification status should be receipt-bound; such a
  status is not part of the runtime commit or fence-release contract;
- the recovery API/UI operations allowed for each unresolved boundary and the
  administrator approval needed to choose roll-forward versus rollback;
- the retention policy for stopped predecessor Docker resources after commit;
  protected and SQLite predecessor history itself is permanent; and
- the supported mechanism for distinguishing a truly external second device
  from loopback, controller-host, proxy, or replayed proof traffic.

Until those decisions and acceptance gates are complete, the 033/034 schema
must remain dormant and unreleasable, and no production rebind claim writer may
be wired into the controller.
