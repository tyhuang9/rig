# M3 controller rebind admission and recovery contract

Status: proposed implementation contract, documentation only. Source baseline:
`cb83fa480c16eec33c8b38e9846ae421139bbce0`. This document resolves the
controller request and recovery choice identified in the [cutover
contract](./m3-rebind-cutover-contract.md#unresolved-implementation-choices).
It neither registers a route nor enables a writer, factory, migration, listener,
or deployment. Existing cutover, ownership, authorization, and acceptance
requirements continue to apply. Review acceptance is not runtime acceptance.

## Decisions

1. Initial admission requires separate configure and rebind confirmations.
   They may be given by the same administrator; their purposes and digests are
   different. Configuring a successor does not first change the current profile.
2. Interactive recovery offers one action, `reconcile`, for one exact existing
   operation. The coordinator derives direction from durable evidence. There is
   no client `force`, `rollback`, `rollForward`, receipt-install, fence-release,
   raw SQL phase, or Docker-resource command.
3. Recovery requires a third, request-scoped administrator confirmation. It
   does not replace either original approver or extend authorization across a
   request, restart, queued job, or changed snapshot.
4. Startup replay uses the existing original claim and independently validated
   durable evidence. It does not treat a recovery audit row as authority and
   does not require a vanished browser session to replay an existing operation.
5. A commit receipt, completed protected commit history, or any immutable
   database-committed event prevents choosing rollback. Cancellation is never
   an undo request. Unknown evidence authorizes no direction.
6. This is one supported-controller implementation and acceptance unit. A0 is
   the first full-controller crash case, not a substitute milestone or a claim
   that the remaining crash matrix is complete.

## Existing implementation and remaining work

| Existing source at the pinned baseline | Reuse and limit |
| --- | --- |
| `internal/controller/server.go` (`requireOperation`), `gateway_upgrade.go` (`upgradeLANGateway`) | Session, administrator, CSRF, canonical request and lock-held same-session reauthorization pattern. No rebind controller route exists. |
| `internal/generatedingress/gateway_rebind_cross_store_proposal.go` (`InspectGatewayRebindProposal`) | Advisory, versioned SQL/protected proposal with predecessor, roster and runtime heads; it is not Docker proof or effect authority. |
| `gateway_rebind_cross_store_prepare.go` (`prepareGatewayRebindLocked`) and `internal/appaccess/gateway_rebind_claim_v2.go` | Fresh SQL/protected proposal comparison, transactional claim/census, post-insert protected preparation. Reuse this path; the controller must not insert the claim directly. Full controller admission must also establish the required preclaim and post-insert Docker proofs. |
| `gateway_rebind_cross_store_commit.go` (`commitGatewayRebind`, `validGatewayRebindPhysicalReconcileRequest`) | Private coordinator, fixed mode predicates and transitions. A narrow controller adapter and live request authorization boundary are still missing. |
| `gateway_rebind_cross_store_recovery.go` (`RecoverGatewayRebindStartup`) | Existing startup reconciliation, current attestation and terminal lock release. It is not an administrator-authorized interactive endpoint. |
| `internal/appaccess/gateway_rebind_approval_authority.go` | Original approval binding and current administrator-role proof are distinct; preserve both. |
| `internal/appaccess/gateway_rebind_recovery.go`, `gateway_rebind_transition.go` | Validated append-only history, actual active phase, committed-event barrier and guarded transitions. Do not derive an effective phase by relabelling stored `unresolved`. |
| `cmd/hostd/gateway_rebind_startup.go`, `main.go` | Startup rebind processing precedes normal workers. Explicit `unresolved` currently refuses in startup dispatch; no complete rebind recovery-only controller surface exists. |
| `internal/generatedingress/gateway_rebind_runtime_composition.go`, `cmd/hostd/gateway_startup.go` | Concrete adapters exist privately; ordinary production construction leaves them dormant. |

The proposal function does not itself observe Docker. The existing legacy
`InspectGatewayRebindPreclaimDockerPredecessor` is advisory and restricted to its
native predecessor format; it cannot be called recursively under writer locks
or treated as a complete V2/repeated-rebind admission adapter. The implementation
must reuse the existing locked ownership/inspection primitives to establish
both required physical proofs for native and rebound predecessors. Whether a
proof is already supplied by a caller must be verified, not inferred from a
method name or a component test.

The coordinator currently accepts prepared/successor-ready/database-committed
recovery combinations, not a generic stored `unresolved` phase. Supporting the
required unresolved-state rows below therefore needs a proof-driven extension
of the existing coordinator and startup dispatch. It must retain the real SQL
phase and immutable events. Merely casting `unresolved` to `prepared`, bypassing
the validator, or exposing the current public startup method is insufficient.

## Proposed controller surface

These names and shapes are proposed additions to `api/openapi.yaml`, with
generated bindings following the existing contract toolchain. They are not
currently registered. All operations require a current administrator and
`Cache-Control: no-store`; all POSTs also require the existing session CSRF
check, including the read-only preview POSTs. No endpoint accepts raw paths,
commands, configs, labels, client-built rosters, or claims of physical proof.

| Operation | Proposed method and path | Meaning |
| --- | --- | --- |
| `inspectLANGatewayRebind` | POST `/api/v1/system/lan-gateway-rebind/proposal` | Read-only preview for an explicit successor; no SQL claim, protected write or Docker mutation. |
| `commitLANGatewayRebind` | POST `/api/v1/system/lan-gateway-rebind` | Start one approved operation synchronously through the existing coordinator. |
| `getLANGatewayRebind` | GET `/api/v1/system/lan-gateway-rebind/{operationId}` | Read one retained operation plus separately qualified current/recovery status. No effects or automatic recovery. |
| `inspectLANGatewayRebindRecovery` | POST `/api/v1/system/lan-gateway-rebind/{operationId}/recovery-proposal` | Read-only exact recovery review; no arbitrary direction selection. |
| `reconcileLANGatewayRebind` | POST `/api/v1/system/lan-gateway-rebind/{operationId}/reconcile` | Run one freshly authorized, operation-pinned reconciliation. |

### Initial request and preview

Preview input contains only `operationId`, `successorProfileRevisionId`,
`successorProfileRevisionNumber`, `successorProfileOperationId`, and
`successorProfile` (`interfaceId`, `selectedIpv4`, `portStart`, `portEnd`),
matching `GatewayRebindProposalInput`. The server independently builds the V2
spec, predecessor checkpoint, roster and heads. Physical readiness is a separate
validated observation and must never be inferred from this proposal alone.
IDs and revision numbers are not permissions or reservations. Validation repeats
the existing canonical UUID, positive revision, explicit IPv4/interface and
approved port-pool rules; no default NIC, address, profile, or wildcard bind.

Preview response contains `version: 1`, the exact V2 spec and reviewable roster,
the two existing canonical approval digests, and `proposalDigest`. The latter
is a versioned, purpose-tagged canonical digest of the complete existing
`GatewayRebindProposalInspection`; it is an optimistic concurrency value,
not a signed capability or a physical attestation the client can assert.
Display only the administrator review fields and digests, never raw Docker
output/config, filesystem paths, credentials, or session data. The full roster
must be reviewable; any paged rendering remains bound to the same snapshot and
cannot silently omit an app from consent.

Commit input repeats the preview input, `version: 1`, `proposalDigest`, and
`configureApprovalDigest` / `rebindApprovalDigest`. The UI presents two distinct
confirmations. The controller computes action bindings itself and obtains both
actor IDs from the authenticated principal, not from request JSON. The configure
digest uses `GatewayProfileSpecDigest`; the rebind digest uses
`GatewayRebindSpecV2Digest`. The existing claim request digest remains unchanged.
No preliminary `ConfigureGatewayProfile` call may move the current SQL head.

Strict decoding rejects unknown/duplicate JSON keys, extra documents, duplicate
query fields, unexpected query strings and mismatched path/body IDs. Mutation
bodies are limited to 16 KiB; oversized requests fail before admission. A preview
has no lifetime that grants authority: every relevant state change invalidates
it even one instant after it is read.

### State and result projection

Keep independently named dimensions rather than collapsing everything into
`ready`: `storedState`, `stateSequence`, `databaseCommitObserved`,
`terminalDisposition`, `selectedCurrentKind`, `currentProof`, `fenceReleased`,
`recoveryPolicy`, and `allowedActions`. `currentProof` is `verified`,
`unavailable`, or `unknown`; absence is reported only after a successful exact
read. Report protected progress as phase/sequence, not as an SQL phase.

An administrator read may show validated historical fields while marking
current proof unavailable. If history itself cannot be validated, return a
sanitized unavailable result and no consent digest or allowed mutation. Never
turn an I/O error into no active operation. The rebind API does not publish LAN
URLs: existing per-app reads independently attest current access. An absent old
address is unavailable, not proof of 404 or withdrawal.

| HTTP outcome | Contract |
| --- | --- |
| 200 | Read/replay, including an identical active-operation replay returning only its nonmutating recovery-required status, or a fully verified terminal result (`committed` or `rolled_back`) with explicit disposition. Active replay returns no executable action until a fresh recovery proposal; a successful abort is not a successful successor cutover. |
| 201 | First admission ended in a fully verified terminal result; `created: true` means the claim was created, not that it necessarily committed. |
| 400 / 413 | Invalid canonical/unknown/duplicate input or oversized body; no admission/effects. |
| 401 / 403 | Initial authentication/CSRF/administrator/approval rejection invokes no reconciliation or effects for this request. Expiry during execution stops further interactive forward work; it does not cancel independently authorized safety withdrawal or later mandatory forward recovery. |
| 404 | Successfully validated history proves requested operation absent; never used for failed reads. |
| 409 | Operation ID reused with a different request, stale snapshot/approval, wrong pinned operation, competing active operation or unsupported requested policy. Requires a fresh review; no automatic resubmission. |
| 503 | Capability dormant, unstable/corrupt/unknown evidence, reconciliation required, release failure or safe continuation unavailable. May include the operation ID and validated state, never an inferred terminal success. |

Use existing problem serialization and fixed domain codes. No asynchronous 202
job is introduced; request timeout or disconnect is not an outcome. GET the
same operation to resolve a lost response. A failed read cannot justify issuing
a new operation ID. Cancellation may still require existing exact-owned safety
withdrawal; it does not permit speculative cleanup or history deletion.

### Idempotency and concurrency

The operation ID and existing canonical request digest identify an admission.
An identical terminal retry reads and freshly proves the retained result without
recreating resources or changing approvals. A matching active operation returns
its pinned recovery-required status; it does not re-enter new admission. Use a
fresh recovery proposal and separate consent to continue. A different body for
the same ID conflicts. New IDs cannot supersede an active operation. Concurrent
requests still serialize through the existing effects lease and gateway locks;
SQLite's immediate transaction and active-claim uniqueness remain decisive.

## Authorization and ordering

For an interactive mutation: authenticate and validate shape/capability first;
then enter the existing deployment-effects lease, Manager mutex, and gateway OS
lock order. Reauthenticate the same session, same actor, current administrator
role and submitted CSRF inside that boundary. Recompute the proposal or recovery
snapshot and all action digests there. The controller must not acquire those
locks and then call a public method that reacquires them.

New admission repeats the exact preclaim proof under that same lease, repeats
the complete authoritative census and both live approvers in the immediate SQL
claim transaction, and performs the post-insert proof before protected/Docker
effects. Keep that lease across the operation's durable terminal or recovery-only
boundary. Each existing effect/transition guard keeps its independent current
authorization and identity checks. Add the interactive session check at the
existing guard boundary; do not perform HTTP work while a long SQL transaction
is held. Do not replace a purpose-bound repository guard with a boolean from
middleware or a previously returned preview.

Any change in digest-bound predecessor lineage/state/checkpoint, successor
selection or IDs, roster/raw grant/transfer, runtime heads or current pending
markers invalidates the preview. Physical topology, owned inventory and config
must independently pass fresh exact proof even if that advisory digest is
unchanged; a digest is not evidence those checks passed. A changed active
claim/sequence/phase, protected history/progress/receipt, selected authority,
current baseline, original approver role or recovery policy invalidates recovery
consent. Logout/session expiry/CSRF invalidation/actor role change denies further
interactive forward work even if hashes match. Existing exact-owned fail-closed
withdrawal remains available under its own safety authority.

Original configure/rebind approvals and their actors are immutable. Both live
roles remain checked where the current serving contract requires them. A new
administrator may inspect the state and submit request-scoped reconcile consent;
that does not replace a revoked original approver. Such a claim remains fenced
until the original authority is valid under existing rules or a separately
reviewed future authority-replacement design exists. No replacement mechanism
is added here.

## Recovery consent and state restrictions

Recovery preview returns `operationId`, `version: 1`, `recoveryDigest`,
`recoveryPolicy`, reviewable state and `allowedActions`. Reconcile input contains
only `version: 1`, `action: "reconcile"`, `recoveryDigest`, and
`recoveryApprovalDigest`; the path supplies the exact operation. No client phase,
direction, terminal receipt, proof or replacement actor is accepted.

Define a distinct purpose `reconcile_lan_gateway`, version 1, over the exact
operation/request digest and validated recovery snapshot plus derived policy.
Bind the actual active state/sequence and database-commit event, protected
history/progress/terminal identity, selected current authority/baseline and
original approval identities/role proof. Reuse existing canonical digest and
validated typed projections; do not hash raw secret config or accept a
client-created snapshot. Recompute under the full lock chain. Ordinary time of
observation is not a state version, and unknown data cannot produce consent.

The third consent authorizes invoking deterministic reconciliation of that
snapshot, not selecting a direction. A new request requires fresh live consent.
Append a purpose-bound `audit_events` record in the narrow appaccess authorization
transaction after exact SQL/role revalidation and before forward effects; audit
write failure refuses the interactive invocation. Record only actor, operation,
canonical digest, policy, validated phase/sequence/receipt digest and timestamp.
No session/CSRF values. Repeated actual attempts may produce multiple audit rows.
An audit row is never replayable authorization or a new progress ledger.

For retained audit integrity, the implementation needs additive resource-scoped
UPDATE/DELETE denial triggers for rebind audit rows in both migration trees,
analogous to migration 026's upgrade audit rules. Existing audit rows are not
globally immutable. This is an implementation prerequisite, not a schema change
made by this document; no separate durable recovery approval ledger is needed.

| Validated durable condition | Reconciliation restriction |
| --- | --- |
| No matching claim | No recovery action; only new separately approved admission when no conflicting history/intent exists. |
| Prepared, no intent or effects | Existing no-effect proof may support abort, or the coordinator may reconstruct the same retained claim and evidence. It cannot mint replacement claims/approvals. Mere missing files are not proof of no effects. |
| Prepared, valid progress before terminal commit, no DB commit | `evidence_decides`: replay exact bound resources; the existing coordinator can prove forward completion or compensation. Client cannot force either. |
| Durable rollback intent / abort receipt, no DB commit | Rollback only, with exact ownership and complete successor-absence proof. After cutover, predecessor address and service must be provable; unavailable NIC cannot count as rollback success. |
| Completed protected commit history or commit receipt, SQL prepared/successor_ready | Forward only: install/read back the matching receipt, then exact SQL transitions and current attestation. No conflicting abort. |
| Any database-committed event, including currently stored unresolved | Forward only; retain successor profile and transfers. Reattest before final transition/fence release; never move the head backward. |
| Stored unresolved before DB commit | No blanket resume. Prove the retained history and any fixed direction, then use explicit unresolved transition predicates. Until coordinator/dispatcher support that actual phase, advertise no executable action. |
| Terminal committed/rolled_back, no active claim | Status/replay and exact terminal/current revalidation only. Do not recreate old serving routes after later redeploy, disable or rebind; use selected current authority and retained history. |
| Conflicting receipts, unbound/foreign resources, corrupt or unstable evidence | No direction choice. Remain fenced/unavailable; only existing independently proved exact-owned safety withdrawal may act. |

In recovery-only startup, expose only the exact pinned operation and necessary
authentication/read/reconcile surface; ordinary deployments, approvals for new
operations and workers stay disabled. Runtime inspection cannot broaden that
operation. A process fail-stop latch cannot be cleared through this API: require
a fresh process to reacquire the complete lock chain and prove startup state.
Pending route/LAN recovery uses its own pinned operation and cannot be bypassed
by invoking historical rebind recovery.

`allowedActions` includes `reconcile` only when complete validated evidence and
live authority make the derived policy executable. Unsupported unresolved
states, denied original authority and unavailable evidence produce an empty
list. A startup safety obligation is not permission granted to a denied request.

After physical success, keep the existing receipt readback, SQL transition,
selected-current attestation, final stable snapshot, fence transition and lock
release ordering. Release failure invalidates response success and may fail-stop
the process without rewriting terminal SQL. No handler manually clears a fence
or releases workers based solely on a returned phase or nil error.

## Implementation ownership and gates

Implementation continues on the same cohesive branch after this design review.
Agree on typed adapter signatures first, then use disjoint ownership:

- Controller/API owner: `api/openapi.yaml` and generated bindings through the
  existing generator; new `internal/controller/gateway_rebind.go` and tests;
  request parsing, exact status/recovery projections and UI action semantics.
- Authority/coordinator owner: narrow additions to existing appaccess rebind
  authorization/audit and additive mirrored migration; ingress adapter around
  existing admission/commit/recovery and explicit unresolved-state proof rules.
  This owner preserves lock/guard ordering and does not duplicate admission.
- Hostd/integration owner: actual startup/recovery-only assembly and reusable
  full-controller child-process harness, using the same handler/composition as
  the supported daemon; no direct private-Manager call presented as that gate.

Local nonactivating implementation and controller contract tests can proceed
while Docker checks are queued. Normal hostd construction, route registration
and factory availability remain closed until the corresponding acceptance and
review gates permit production wiring. Test composition must be explicit and
unavailable through a release binary flag, environment variable or public API.
Do not claim a built but unregistered handler is accepted supported-controller
behavior. Production wiring and release are separately gated, not implied by
merging documentation or passing a mocked test.

The concrete runtime used by the full-controller Docker harness must first pass
its actual typed forward/rollback/replay and independent cleanup gates on the
candidate source. Those results may reveal internal runtime repairs without
invalidating the external authority contract; any changed public invariant
requires revisiting this design. All cutover gates, including the full crash
matrix, race checks and physical second-device qualification, remain required.
The second-device gate is platform qualification, not an extra per-rebind
runtime commit prerequisite. Existing private/process/component successes do
not inherit acceptance for the new controller assembly.

## Concise verification and rollback plan

| Verification group | Required evidence |
| --- | --- |
| Capability/authentication | Missing runtime denies before claim/audit/effect; viewer, missing/expired session, bad CSRF, revoked original or current admin, wrong-purpose digest, unknown/duplicate input and stale proposal all refuse. Positive control uses the same real repository and handler. |
| Admission/concurrency | Exact two-approval request commits via existing coordinator; same-ID replay is nonmutating, changed-ID/body conflict and competing writer/route/LAN batch/claim cannot cross census. Prove same-lease preclaim, transactional census, post-insert reattestation and fresh guard invalidation. |
| Recovery | Table-driven coverage of every row above; snapshot change after preview refuses; audit is not authority; explicit unresolved history remains real; DB-committed then unresolved never rolls back; protected commit/rollback direction cannot be overridden. Audit retention and migration upgrade/downgrade refusal controls use existing repository tooling. |
| Response/lifecycle | Lost ACK, cancellation before/after durable writes, late role revocation, ambiguous protected install, current-attestation failure and lock-release failure never report unwarranted success, URL or worker release. Check exact retained SQL/protected history, both latches and effects. |
| Full controller | Authenticated real request through actual controller assembly, real SQL/protected storage and Docker executor. Kill and reap the entire child after A0 first, then every durable write/effect; fresh process startup before workers must deterministically replay, exactly compensate or remain recovery-only. Include cross-process contender and terminal release faults. No sleeps or private callback alone establishes this. |
| External/platform | Exact hosted Docker forward/rollback/replay/cleanup and race selections; real second device for each roster app, redeploy/restart, same-NIC DHCP drift and old NIC removal/return. Record absent-address limitations and external DB application path; namespace/loopback probes do not replace the device. |

Use current canonical generation, focused tests, formatter/vet/build/docs and CI
selection checks; discover exact test names and reject missing/skip outcomes.
Run the smallest meaningful controls before the expensive full-controller
matrix. Record source pins, child exits, kill/reap, evidence and untested limits.

During development, rollback is removing the dormant handler/adapter change.
After a real operation has started, code rollback is not data rollback: never
drop 033/034 or new audit history, undo a committed profile/transfer, delete
protected evidence, clear latches, or remove guessed resources. Disable new
admission, retain recovery-capable compatible code and exact pinned recovery;
after database commit only forward repair is valid. No managed database service
or schema ownership beyond the existing controller metadata is introduced.

## First implementation checkpoint

The initial design review was documentation only. The first dormant code
checkpoint now adds the canonical recovery review and request-scoped
`AuthorizeAndAuditGatewayRebindReconcile` repository method. It uses the existing
validated SQL recovery reader under `BEGIN IMMEDIATE`, checks the current
recovery actor and both original approvers, and commits an attempted-consent
audit only after exact snapshot and purpose-bound approval validation.

The protected scan/current projection is externally validated input from the
future locked ingress adapter. SQL validates its structural coherence and binds
its digest; SQL does not prove protected bytes, Docker ownership or serving.
The returned audit result is not an effect capability. Terminal/no-active
operations are deliberately refused by this first method and remain a later
facade responsibility. Actual stored unresolved phases are retained.

Mirrored migration 036 prevents update and deletion of all audit events whose
resource type is `lan_gateway_rebind`, including pre-existing and future action
names. It does not rebuild the audit table or change unrelated retention rules.
Rollback retains this additive audit protection and all existing history.

The pre-edit native Go 1.26.7 baseline passed five exact parents and two leaf
cases. The focused post-edit native gate passed six exact parents, 29 descendant
test nodes and 26 leaf cases, with no failures or skips. The six-parent selection
contains the four new authorization/migration parents, the existing exact V2
prepared-claim parent, and the all-migrations mirror parent. The recorded native
execution retained all 889 Go/SQL/module input hashes unchanged. Controls cover
A0 validated absence, actor-bound consent, stale state/digests, independently
revoked approvers, cancellation before audit, insert and commit failure,
forward/rollback restrictions, contradictory successor generation/intent/state,
terminal refusal, and resource-scoped audit retention.

The read-only formatter and diff checks, scoped `go vet` for appaccess/database,
and the four production CLI builds also passed with unchanged source pins.
No production binary was launched.

The focused source/security review accepted the first checkpoint after adding
closed current-state version/revision validation and exact successor
protected-generation/intent coherence. The retained command, output and review
receipts are under `temp/m3-controller-rebind-contract-20261012/` outside the
checkout. This checkpoint does not establish cross-platform or race acceptance,
controller request assembly, Docker behavior, a full-controller crash boundary,
physical LAN qualification, production wiring or M3 completion. Those gates
remain required above; passing an advisory SQL authorization test cannot replace
them.
