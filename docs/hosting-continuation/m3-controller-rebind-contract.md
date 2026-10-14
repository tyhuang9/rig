# M3 controller rebind admission and recovery contract

Status: contract with dormant implementation checkpoints; the supported
controller flow and its acceptance gates remain incomplete. Source baseline:
`cb83fa480c16eec33c8b38e9846ae421139bbce0`. This document resolves the
controller request and recovery choice identified in the [cutover
contract](./m3-rebind-cutover-contract.md#unresolved-implementation-choices).
The branch includes the additive audit migration described below, but registers
no rebind route and enables no writer, factory, listener, or deployment.
Existing cutover, ownership, authorization, and acceptance
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

## Request parsing and invocation-local forward guards

The next local checkpoint adds private proposal, commit and reconcile request
parsers in `internal/controller/gateway_rebind_request.go`. They accept only the
contract's exact fields, one JSON object and canonical values. Duplicate and
unknown keys, nested ambiguity, extra documents, query strings, actor or proof
injection, and client-selected recovery direction are refused. The actual body
stream is limited to 16 KiB independent of Content-Length; an oversized request
returns a typed size error for eventual handler mapping. Reconcile operation
identity comes only from the matched path. The parsers register no routes and
their returned values confer no authority.

An optional session guard now belongs to each private physical-driver
invocation, never to the Manager or shared state. Forward reconciliation,
confirmation, completed restoration and committed-current attestation check it
around existing SQL/protected authority reads. A callback that cancels its
context while returning nil is also refused. The original nil-guard path is
retained for startup/private callers. Rollback and exact-owned withdrawal keep
their existing independent safety authority, including withdrawal after an
interactive session has been revoked.

The pre-edit ingress baseline at local checkpoint `20e58b2` passed three exact
parents through actual WSL execution. The focused changed-source gate then
passed four parents and ten descendant subcases (twelve leaves including the
two parents without subtests). It covers separate concurrent invocations,
revocation before and after authority reads, cancellation, nil/allowed current
attestation, revocation during observation, exact-owned withdrawal with SQL and
protected history unchanged, and retained completed-successor restoration.
Cross-compilation, binary discovery and Linux execution exited zero. The
compiled binary and all 892 Go/SQL/module input hashes remained unchanged.

Native Windows parser verification passed five exact parents, 141 descendant
test nodes and 136 leaf cases with no failures or skips. This includes four new
parser parents and retained profile/body-validation coverage. The tests prove
typed-value preservation, bounded reads and closure, strict decoding, canonical
identities/digests, and zero-valued sanitized failures. Scoped controller and
ingress vet checks and the four native production CLI builds exited zero.

The first static wrapper retained a failed formatter assessment: five existing
Go files had CRLF line endings. A read-only comparison proved that canonical
gofmt output differed only by CRLF-to-LF normalization. Only those line endings
were changed; a separate final formatter check was empty and diff checking
passed. The original failed assessment and successful vet/build receipts remain
preserved. Behavior was not rerun for this semantics-preserving line-ending
correction.

Security/source review accepted the guarded core after the explicit
post-callback cancellation check and accepted the private parsers. All three
new ingress parents map exactly once to the existing `rebind-history` partition
in each required workflow; no CI file changed. Exact commands, raw output,
source manifests, review records and formatting evidence are retained under
`temp/m3-controller-rebind-contract-20261012/` outside the checkout. Documentation
checks for this frozen page are recorded separately by the integration owner.

This remains preparation within one cohesive controller implementation. The
following checkpoints add session binding and strict admission proofs; handlers,
the lock-held audit/executor facade, and complete actual-phase recovery
predicates remain unfinished. No route or production writer is activated.

## Live session binding

Local checkpoint `269bfc1` adds `gateway_rebind_session.go` and its tests. The
private binder derives the actor from the authenticated principal and captures
one original session cookie, the submitted CSRF value, the authentication
service, and the original request context. Initial validation and every guard
invocation require the same live actor, administrator role, and CSRF value.
Both the invocation context and original request context are checked before
and after authentication. A detached forward context cannot revive a canceled
interactive request. Failures are sanitized; no token, CSRF value, or request
authority is persisted or shared between requests.

The binder does not grant effects authority or register handlers. Startup and
exact-owned withdrawal retain their separate authority. Native Windows Go
1.26.7 verification passed five exact parents and 34 descendant cases, without
failures or skips. Discovery, execution, and scoped controller vet exited zero;
all 895 Go/SQL/module inputs remained unchanged. Source/security and final
review accepted the two-file checkpoint. The original commit metadata receipt
and its additive corrected tree assessment are both retained.

## Strict admission and prepared-prefix proof checkpoint

`gateway_rebind_admission_proof.go` and the shared preparation core now provide
the private strict admission path. The caller must hold the deployment-effects
lease, Manager mutex, and gateway OS lock. The strict entry requires both a
physical proof reader and live-session guard; neither has a permissive default.
It rechecks the session, freshly compares the submitted proposal and selected
authority, validates the preclaim physical proof, and checks the session again
immediately before the existing transactional claim. After the SQL claim, it
requires the exact prepared claim, canonical initial event, roster, runtime
heads, and selected current authority. It compares a fresh physical proof to
the preclaim proof and repeats the SQL/current census before the final session
check and first protected write. A refusal after claim retains A0 and its
fence, without installing protected evidence or authorizing Docker effects.

The proof binds the immutable native or rebound predecessor identity and full
validated observation, the derived current target where applicable, the exact
checkpoint, and the successor profile and network inventory. Native Docker
observations and successor network observations are independently sampled
twice. Rebound proof uses the existing current-predecessor observation and
physical-inventory readers. The production wrapper unconditionally supplies
the existing real Docker inspector and network readers. A mandatory private
read seam permits deterministic fixtures while retaining the same target,
checkpoint, ownership, digest, stability, and network-selection predicates.

Prepared recovery now applies the real proof wrapper to A0 and incomplete
checkpoint/intent prefixes before repair writes. It requires stable SQL and
physical evidence and does not cast an unresolved phase to prepared. An exact
complete checkpoint/intent/progress-1 replay returns without new physical
reads or writes. The claim-bound intent is validated before the first
protected write, and existing protected contents are compared exactly.
The legacy private preparation entry remains for component composition;
there is no supported controller route into it. Future controller composition
must call the strict entry with the concrete proof reader and live guard.

Moving network validation before the checkpoint exposed a retained process
test that had used the network observer as a post-write hook. A private
package-test hook now runs immediately after the actual exact checkpoint
write/readback and before intent-store creation. It is uninitialized in
production and sanitizes callback failures like the existing after-intent
hook. The test retains actual child kill/reap and exact SQL, fence, protected
prefix, and recovery assertions; it does not synthesize the checkpoint itself.

### Executable evidence and limits

The final focused Go 1.26.7 gate cross-compiled the ingress test binary,
discovered the exact selection, and executed it under WSL. All 12 parents and
24 descendant cases passed once each, with no failures or skips (30 leaves,
including parents without descendants). Compilation, discovery, and execution
exited zero in 8.406 s, 3.609 s, and 14.922 s respectively. All 896
Go/SQL/module inputs and the compiled binary hash remained unchanged.
The immutable receipt is `admission-corrected3-wsl.result.json` under
`temp/m3-controller-rebind-contract-20261012/` outside the checkout.

The selection covers four new strict-admission/recovery parents, six affected
existing preparation/process parents, and two retained census/preflight
parents. Controls include missing proof/session, revocation and cancellation,
native and rebound success, malformed immutable/derived physical facts,
second-observation drift, initial-event and runtime-head changes, stable A0
repair, and exact completed replay. Real child-process tests retain claim,
checkpoint, intent, and progress-1 kill/reap boundaries. These children invoke
the private Manager path with fixture physical evidence; they are not the full
supported controller, actual Docker, or physical-LAN acceptance gates.

All earlier failed attempts remain retained. The first behavior run exposed
incomplete fixture setup; the next attempt failed compilation on a nonexistent
test-only timestamp field. The following behavior run reached all cases and
exposed private-directory mode, inconsistent fixture network inventories, and
the obsolete checkpoint hook location. Corrections used the existing private
directory preparation helper, the actual immutable profile approval timestamp,
consistent fixture observations, and the real post-write hook. No production
ownership predicate, immutable intent comparison, or expected positive outcome
was relaxed to obtain the pass.

Source/security review accepted the fixed production adapters, mandatory
private seams, and process-local selection-handle rebinding. The four new
parents each map exactly once to `ingress-remainder` in the existing required
Linux and Windows workflows; no CI source changed. Final formatter, diff,
scoped vet, four-CLI build, documentation checks, and aggregate review receipts
are retained separately in the same unit's evidence directory. These checks
do not qualify the still-unwired controller facade, real Docker execution,
full-controller crash matrix, second-device LAN behavior, or M3 completion.

## Dormant commit facade and historical retry checkpoint

This checkpoint adds `Manager.CommitGatewayRebind` and dormant controller
preview/commit handlers. No route, capability factory, or public writer is
registered. The handlers are directly tested with a runtime mock; this is not
an end-to-end supported-controller or physical Docker acceptance claim.

The server builds a versioned review from the complete canonical proposal,
roster, runtime heads, predecessor, selected source, and checkpoint bindings.
Configure and rebind approval digests retain separate purposes. The pure public
review validator checks the complete review and all digests; it grants neither
physical proof nor effects authority. The controller compares the submitted
candidate to this server review, derives the actor from the authenticated
principal, and passes the request's live session guard. Curated response DTOs
exclude protected paths, Docker/configuration output, and session data.

The commit facade holds one deployment-effects lease, Manager mutex, and
gateway OS lock across request classification, strict admission, continuation,
and terminal proof. It constructs concrete production drivers and attaches an
invocation-local forward-session guard. Fresh admission uses the preclaim and
post-insert physical proof described above. A pure accepted-claim validator
recomputes both canonical request digests before an accepted token can grant
continuation or cleanup authority. `created == false` returns to read-only
replay classification; it cannot enter this invocation's continuation or abort.

No-effect abort is restricted to this invocation's newly created, canonical
prepared claim with exact checkpoint, unchanged approvals, no intent/progress/
terminal evidence, and fresh session and no-effect proof. A0 without a
checkpoint, revoked or canceled sessions, changed claims, and uncertain
protected prefixes remain fenced. The existing coordinator continuation and
release ordering are shared without weakening rollback or exact-owned
withdrawal authority. A lock-release failure returns a zero response.

Historical completion and current authority are separate. An exact terminal
retry does not replay effects. A newer active claim leaves the historical
operation terminal but its physical proofs unavailable; the historical
operation's released fence must not be confused with the newer global fence.
A later same-lineage revision or another current authority is not the old
operation's original terminal baseline. Active replies expose the actual SQL
phase and database-committed state and do not claim terminal physical proof.
Controller 201 responses require a newly created terminal operation, both
verified proofs, and released operation fence; unknown or inconsistent runtime
projections are rejected.

### Read-only current and terminal proof

The current-proof helper requires a stable no-active SQL snapshot, complete
hosting/runtime/LAN census, exact selected authority, protected history and
file identities, live session, fence, and stable latch state. It uses the
existing validated predecessor observer for native, rebound, and valid stopped
current state. Three complete hosting snapshots and final protected-file and
session/fence checks prevent a stale partial proof. Its digest binds actual
lineage, hosting snapshot and physical observation; it is not permission to
serve or proof that historical approvers remain administrators.

A committed terminal proof validates the retained P17 result and receipt,
then proves the exact successor is serving and the complete predecessor chain
is retired. The concrete read-only retirement observer runs before and after
the serving attestation. It accepts the existing stopped-or-absent retirement
contract and does not require retired resources to equal their historical P17
snapshot. The digest binds retained terminal evidence, the complete retirement
action, fresh serving proof, and current proof. Abort proof uses the existing
read-only rollback observations and binds exact stage absence. None of these
reads stops, repairs, retires, reconciles, writes history, or changes latches.

The admission predecessor, current-proof and terminal-response implementations
have mandatory private dependency cores for deterministic component tests.
Every production wrapper unconditionally constructs concrete dependencies;
there is no public, Manager, or global override and no optional fallback.
Positive component tests use the actual managed runtime and readers with
controlled probe transport. A separate exported commit control rejects a
missing concrete current inventory before host probes, with unchanged SQL,
protected history and effects. That control proves refusal behavior; source
review establishes unconditional production factory selection. Running
exported commit acceptance still requires actual Docker and host probes.

### Checkpoint verification and retained failures

The review builder passed four WSL parents and 27 descendant cases. The dormant
controller handlers passed eight native Windows parents and 66 descendant
cases, including full review/candidate binding, session revocation, active
versus terminal projections, native current authority, and refusal of an
unproved 201 response. The accepted-claim application layer passed its focused
canonical-digest controls. Exact commands and immutable source/log receipts
are retained under `temp/m3-controller-rebind-contract-20261012/` outside the
checkout.

Earlier failed attempts are preserved. Moving physical checks before the
checkpoint exposed two obsolete coordinator failure-injection locations;
commit `1ff1256` moves only those injections to the actual post-checkpoint hook,
retaining all rollback and history assertions. The first facade aggregate
revealed a new direct-abort test using the process-global commit barrier.
The fixture now owns private latches and asserts the expected armed barrier;
production latch behavior is unchanged. Its replacement passed all seven
coordinator parents and 16 of 17 selected parents. Source inspection identified
real host probes as an independent fixture testability limitation; the generic
runtime failures did not establish that a host probe caused those failures.
The private-core adjustment above keeps the production probe path unchanged.
The following 22-parent gate passed 21 parents. Source tracing then identified
a fixture restart-config mismatch: it returned a multi-entry directory archive
for a request to copy one active file. A correction uses the existing
single-entry archive helper with the actual retained active filename and
bytes, changing no production validator. The targeted replacement still
refused the positive case. A subsequent test-only diagnostic observed the
`runtime_owned_census` checkpoint before archive inspection: the inherited
runner answered an exact volume-user query with predecessor container names
instead of the required final container ID. Selection, hosting SQL census and
target validation succeeded. This evidence identifies the observed earlier
fixture mismatch; it does not establish a production ownership defect.
After the exact volume-query response was corrected, diagnostics observed
successful inventory read and serving attestation but a refusal in the local
admission matcher. The runtime proof holds the canonical image ID; the validated
Docker inventory retains its `sha256:` prefix. The trace established unequal
raw strings and equal canonical identities. The matcher correction binds both
representations to the exact target image and retains pinned-image validation,
including platform and approved repository digest. This local admission defect
is distinct from the published Docker current-attestation failure under
separate investigation. No production identity check is dropped.
The final fixture census also answers exact retained-native-predecessor volume
queries from actual present container mounts, and its controlled transport
models listener absence only when the fixture's existing state proves it.
All other commands and live/unknown endpoints delegate unchanged. The clean,
uninstrumented terminal parent and its three cases then passed under WSL:
fresh verified proof, stopped-successor refusal, and revived-predecessor
refusal. Compilation, discovery and execution exited zero in 8.547 s,
2.078 s and 22.547 s, with all 906 Go/SQL/module inputs and the binary unchanged
(`commit-facade-predecessor-volume-fixture-wsl.result.json`). The image identity
positive and all eight malformed/mismatch controls passed in the separately
retained targeted run; that run's failed terminal cases remain recorded.
Current-proof fixture compile and lock-admission failures are retained with
their narrow corrections; no failed whole run is relabelled as passing.

Security and scoped root source reviews accepted the final production and
fixture changes. All 25 new ingress parents map exactly once through the
unchanged required CI selector. Final canonical formatting and diff checks
passed for all 19 pending Go files. Scoped vet for appaccess, controller and
generatedingress and the four CLI builds passed in 51.875 s and 44.578 s, with
906 unchanged inputs (`commit-facade-image-static-native.result.json`). The
finite diagnostic markers were removed and production hashes restored before
clean execution; no diagnostic instrumentation is part of this checkpoint.

The final combined affected-contract execution is recorded separately in
`commit-facade-final-wsl.result.json`; its outcome is not inferred from the
partial or targeted receipts. Documentation verification runs `check:workflow`,
`build`, and `check:accessibility` on this frozen page, with its result retained
by the integration owner rather than rewriting the page to certify itself.
Full supported
controller route/factory composition, interactive reconciliation and actual-
phase recovery, full-controller crash coverage, actual Docker terminal replay,
and physical second-device LAN qualification remain incomplete. These are
required follow-on implementation and acceptance work, not inferred passes
from this dormant checkpoint. Rollback of this unactivated code does not remove
retained SQL/audit/protected history.

## Dormant operation recovery checkpoint

This checkpoint adds private controller handlers for operation status, recovery
review and reconciliation, backed by separate generated-ingress read and
recovery capabilities. The recovery repository cannot create a claim. The
handlers remain absent from production route registration and hostd factory
assembly; their presence does not open the production wiring gate above.

GET status rechecks the original administrator session before disclosure.
Recovery preview and reconcile additionally require the original request's
CSRF proof. Status distinguishes actual SQL phase from the latest validated
protected progress phase, historical completion from selected current
authority, and verified absence from unavailable evidence. Only a successful
complete-history absence check maps to 404. Raw repository not-found errors
remain unavailable. Pristine installation absence additionally requires exact
empty SQL, a complete read-only protected-file census, canonical empty route
state and a clear fence, all stable across repeated reads. Partial, unknown or
corrupt artifacts remain unavailable; the effect-authority history scanner
keeps its predecessor requirement. Responses omit audit capabilities, raw SQL
events, protected paths and bytes, Docker state, and session material.

Executable recovery review holds the deployment effects lease before the
gateway locks. Its canonical consent binds the actual claim, roster, runtime
heads, protected scan, selected current evidence, actor and derived policy.
Reconcile recomputes that review under the same lock order, uses the existing
transactional authorization audit, and repeats the complete review and live
session checks before dispatch. An audit row records attempted consent; it
cannot authorize an effect or a later request by itself. Physical boundaries
use the repository supplied to that invocation.

Stored `unresolved` is retained as the actual phase. A fixed pre-DB commit
prefix follows the existing successor-ready transition; a fixed pre-DB abort
follows rollback. A retained DB-commit event permits only forward completion.
Unfixed or contradictory unresolved evidence has no executable action. A
validated checkpoint-only no-effect abort has an explicit canonical evidence
case: no invented intent or progress, exact generation and scan/receipt
digests, and abort disposition only. Its exact checkpoint, receipt and
no-effect ownership are revalidated before completing terminal SQL.

Newly requested physical rollback checks the live request and original
approvers before its directional start. Existing mandatory compensation,
retained terminal completion and exact-owned safety withdrawal retain their
independent safeguards after request expiry. Recovery does not clear process
latches, reverse committed transfers, rewrite immutable history or replace a
claim or its original approvals. A successful reconcile response requires an
inactive terminal operation, matching protected and SQL receipts, the
policy-consistent disposition, released operation fence, and fresh qualified
terminal and current proofs.

### HTTP acceptance assembly and verification status

Local controller tests explicitly assemble the dormant handlers with real
authentication, SQLite, the repository and Manager for exact absence and
refusal paths. Mock-runtime handler tests separately cover curated response
and authorization boundaries. Neither is a positive physical recovery gate.

`TestLiveGatewayRebindControllerRecoveryCommitAndReplay` reuses the existing
disposable GI Docker fixture. Its parent compiles and discovers one exact
controller child before creating resources, retains the real prepared P1
prefix, and owns cleanup. The child consumes only the parent's private
ephemeral fixture manifest and uses real authentication, HTTP, repository,
Manager and Docker execution. It exercises recovery preview, refused altered
consent, forward reconciliation, terminal reads and stale-request refusal.
The parent checks terminal SQL/protected state, retained prefixes, grants,
runtime heads and both routes, then the existing independent cleanup census.
This is a controlled test protocol, not an external prepared-state interface.
The child emits only finite markers immediately before its HTTP calls. On
failure, the parent reports the last recognized attempted stage and suppresses
raw child output; that marker does not identify the refusing predicate or
prove that an HTTP call completed.

The live parent is selected explicitly by the Docker workflow, including its
separate opt-in and exact RUN/PASS/no-skip checks. Actual Docker execution is
unrun locally. It remains an acceptance requirement on the exact candidate;
the parent/child source does not establish full-controller kill/reap coverage
at A0 or later boundaries, production assembly, or second-device LAN access.

### Local verification and limits

The final focused Go 1.26.7 Linux execution passed all 13 selected parents and
86 descendants, with every expected RUN/PASS present once and no failures or
skips. Compilation, discovery and execution completed in 8.594 s, 3.968 s and
157.625 s. All 925 Go/SQL/module inputs and the compiled binary stayed unchanged
(`recovery-flow-final-wsl.result.json`). This is actual WSL execution of the
cross-compiled Linux binary, without race instrumentation; it is not a Docker
run. The unchanged CI selector places eight parents in `rebind-history`, the
literal unresolved physical replay parent in `rebind-typed-completed`, and the
four pure validator parents in `ingress-remainder`.

The audited component gate includes prepared commit, fixed abort, and literal
unresolved commit before and after the immutable DB-commit event. Each positive
requires exactly one audit, the actual shared coordinator, the expected terminal
SQL state and disposition, released fence, and validated current and terminal
proofs. Managed physical readers use controlled command/probe transport; no
successful proof or response is fabricated. Adjacent controls cover invocation
repository drift, post-audit drift/session loss, missing dependencies, actual
effects-lease contention, original approval revocation, canonical claim/roster/
runtime-head binding, pristine absence, and truthful unresolved direction.

A meaningful two-case RED first showed that cached commit and abort replay
refused literal pre-DB `unresolved`. The only input changed for its GREEN was
the precommit authority predicate's phase allowlist; every existing DB-event,
claim, approval, current-source and serving-census predicate remains. Both cases
then passed with unchanged unresolved SQL and no new effects
(`recovery-unresolved-physical-red-wsl.result.json` and
`recovery-unresolved-physical-green-wsl.result.json`). Refusal-side owned safety
withdrawal in the RED is retained as failed-run evidence.

Separate retained gates passed six appaccess parents/51 descendants natively
and 20 controller parents/99 descendants in WSL, including the real initialized
empty-state HTTP path. Their receipts are
`recovery-authority-first-native.result.json` and
`recovery-handlers-final-wsl.result.json`. Final canonical formatting passed for
35 Go files; diff check, scoped vet for appaccess/controller/generatedingress,
and all four CLI builds passed with the same 925 unchanged inputs
(`recovery-flow-static-native.result.json`; vet 69.750 s, builds 66.157 s).

Earlier compile, fixture, environment and behavioral failures remain in their
original receipts. These include the native fixture ACL failure, the initial
empty-state projection refusal, the active disposition omission, and the
path-specific single-file versus directory archive mismatch in the controlled
runtime fixture. Temporary finite diagnostic markers were removed before clean
verification. Missing marker output did not prove non-entry, and a zero failed
response did not prove the retained SQL phase. The decisive runtime trace and
subsequent passing managed-component run supersede those early inferences.

Documentation workflow, build and accessibility results are retained separately
against this frozen page. The earlier checkpoint results above remain evidence
only for their recorded source. Actual Docker HTTP recovery and cleanup, hosted
race acceptance, production hostd assembly, the full supported-controller crash
matrix and physical second-device LAN qualification remain required and unrun
for this local checkpoint. None is inferred from local component success.
