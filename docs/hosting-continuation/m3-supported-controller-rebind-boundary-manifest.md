# Supported controller rebind crash boundary manifest

Status: candidate source contract, 2026-10-14. This document defines the
required full-hostd crash and recovery evidence. It does not record a passing
Docker run or physical LAN qualification.

The acceptance action is an authenticated request to the handler assembled by
the real hostd startup path. The child process uses the same SQLite database,
protected history root, generated-ingress Manager, Docker runner, owner lease,
deployment-effects lease and gateway OS lock as production. A test callback may
observe or block a validated boundary, but it grants no effect authority and is
not exposed through flags, environment, HTTP or generated API contracts.

`hostdDaemonReady` means that the listener and production handler have been
assembled. The parent must complete an authenticated HTTP probe before it
sends a proposal, commit, status, recovery preview or reconcile request. A
ready callback alone is never API readiness.

Each crash case follows the same protocol:

1. Build and discover the exact child test before creating Docker resources.
2. Create a disposable baseline with real SQLite, protected history and Docker
   serving state. The parent registers cleanup independently of every child.
3. Start hostd through the shared production assembly and prove authenticated
   HTTP readiness.
4. Submit the initial proposal and commit through the canonical HTTP routes.
5. Hold only after a durable read or a specific completed effect proves the
   named boundary. Fixed sleeps and a marker alone are insufficient.
6. The parent sends `SIGKILL`, reaps the exact child, and starts a fresh child
   from the same compiled binary and source identity.
7. Recovery uses only the exact pinned operation's status, recovery proposal
   and reconcile routes when recovery-only startup is selected. New proposal
   and commit remain closed in that process, including after reconciliation.
8. A second fresh process must prove normal startup. It verifies retained SQL
   order and audit, protected digests and receipts, selected authority, runtime
   heads and grants, serving or withdrawal, fence and latch state. It also
   proves the seeded ordinary job stayed queued. The recovery-only assembly's
   direct construction also proves that no worker, auto-deploy runner or relay
   graph is started; auto-deploy and relay are not independently seeded live
   effect sentinels in this matrix.
9. The parent removes and rechecks exact containers, networks, volumes, images,
   builders, adapters and child processes even when setup, crash or restart
   fails.

The executable source of truth for exact IDs and source anchors is
`cmd/hostd/gateway_rebind_live_manifest_test.go`. Its untagged control test
rejects missing, duplicate, unknown or skipped manifest entries.

## Ordered durable writes and effects

| ID | Required proven boundary | Recovery direction |
| --- | --- | --- |
| `a0-sql-claim` | Prepared SQL claim exists while checkpoint, intent, progress and terminal are absent. | Rollback only after fresh exact no-effect proof. |
| `p0-predecessor-checkpoint` | Exact predecessor checkpoint is durable and readable. | Rollback. |
| `p1-protected-intent` | Protected intent is digest-bound to A0, checkpoint, roster and runtime heads. | Rollback. |
| `p01-successor-intent` | Protected progress sequence 1 records successor intent. | Rollback. |
| `p02-image-observed` | Sequence 2 binds the immutable image and stage plan. | Rollback. |
| `p03-network-bound` | Sequence 3 binds the successor network and ownership. | Rollback. |
| `p04-config-volume-bound` | Sequence 4 binds the config volume. | Rollback. |
| `p05-data-volume-bound` | Sequence 5 binds the data volume. | Rollback. |
| `p06-stage-container-bound` | Sequence 6 binds the stopped stage container. | Rollback. |
| `p07-stage-config-intent` | Sequence 7 precedes the stage config copy. | Rollback. |
| `p08-stage-config-copied` | Sequence 8 attests copied stage config bytes. | Rollback. |
| `p09-stage-start-intent` | Sequence 9 precedes the exact stage start. | Rollback. |
| `p10-stage-serving` | Sequence 10 binds the serving stage endpoint. | Rollback. |
| `p11-final-config-intent` | Sequence 11 precedes the final config copy. | Rollback. |
| `p12-final-config-copied` | Sequence 12 attests copied final config bytes. | Rollback. |
| `p13-final-handover-intent` | Sequence 13 binds predecessor observation and application networks. | Rollback. |
| `p14-final-container-bound` | Sequence 14 binds the final successor container. | Rollback. |
| `p15-cutover-intent` | Sequence 15 binds the exact cutover observation. | Rollback. |
| `p16-successor-serving` | Sequence 16 proves successor serving after cutover. | Rollback unless later immutable commit evidence fixes forward direction. |
| `p17-handover-completed` | Sequence 17 proves resources, final serving and predecessor withdrawal. | Forward. |
| `r01-rollback-intent` | Rollback intent binds the exact-owned cleanup set and predecessor route. | Rollback. |
| `r02-rollback-completed` | Rollback completion proves owned resources absent and predecessor serving. | Rollback terminal. |
| `s01-protected-terminal-installed` | Exact protected terminal receipt is durably installed after the physical result. | Forward. |
| `s02-protected-terminal-readback` | Terminal receipt reads back with the exact operation, generation, sequence and digest. | Forward. |
| `s03-successor-ready` | SQL `successor_ready` follows terminal publication and fresh physical serving confirmation. | Forward. |
| `s04-database-committed` | One SQL transaction records `database_committed`, the selected profile and grant transfers. | Forward only, including literal `unresolved`. |
| `s05-current-attestation` | Selected-current physical attestation matches the exact terminal lineage before terminal SQL. | Forward. |
| `s06-terminal-sql` | Terminal SQL retains the receipt, immutable event, original approvals and current attestation. | Forward. |
| `s07-stable-final-census` | Repeated SQL, protected, current, hosting and runtime census is stable. | Forward. |
| `s08-fence-transition` | Active claim is absent and the rebind fence is clear. | Forward. |
| `s09-effects-lease-release` | Deployment-effects lease release succeeds first. | Success remains withheld on ambiguity. |
| `s10-gateway-lock-release` | Gateway OS lock release succeeds after the effects lease. | Success remains withheld on ambiguity. |
| `s11-response-loss` | The terminal response is dropped after both releases. | Fresh replay returns the retained result without effects. |

The protected terminal store emits `s01` only after a fresh exact durable write
and before its mandatory decrypting readback. It emits `s02` only after that
readback and complete protected-history confirmation. Existing replay,
conflict, and prewrite failure emit no false install event. The two cases are
therefore distinct crash positions. Likewise, the gateway-release event is
emitted only after the true release:
`s10` proves that completed release, while `s11` kills the blocked handler
before its HTTP result can be delivered.

The Docker runner also covers lost acknowledgments after network, config volume,
data volume, stage container, stage config copy, stage start, final config copy,
final container, cutover and successor serving effects. Recovery must inspect
exact ownership and completed physical state before it repeats, adopts, removes
or refuses an effect.

## Required adversarial and restart cases

The same manifest requires both fixed precommit `unresolved` rollback and
forward-only `unresolved` after an immutable database commit. It also requires
terminal replay, selected-current serving restoration, and exact operation
precedence for pending route, LAN and LAN batch recovery.

Terminal replay compares the exact operation-labelled container, network and
volume IDs before and after a response-loss restart. Selected-current
restoration stops the committed final container, then requires fresh startup
to restart that same immutable container before normal HTTP admission and both
roster applications become reachable.

Authority and concurrency cases cover a competing hostd, original approver
revocation, current administrator revocation, cancellation before and after a
durable write, Docker ownership drift, protected/config drift and hosting or
runtime census drift. Failure cases cover both lease releases and a failed
exact-owned emergency withdrawal. None may clear the fence, invent absence,
delete an unproved resource, or expose another operation through recovery HTTP.

The prewrite cancellation case first completes proposal review, arms a private
child-only barrier, sends the commit over authenticated HTTP, and cancels only
after the server reaches a read-only Docker admission check before A0. The
postwrite case cancels after the A0 SQL claim readback while the process stays
alive. The current-role case obtains both recovery digests as an administrator,
then revokes that live session before submitting reconcile with those exact
digests; the denied request must leave the operation active and unchanged.

The recovery process must retain its restricted routes after a terminal result.
The live sentinel proves an already queued ordinary job remains untouched. The
source anchor proves `runRecoveryOnlyController` directly constructs only the
pinned controller server and does not call the ordinary worker, auto-deploy or
relay startup paths. This matrix does not claim independent live auto-deploy or
relay effect sentinels. Normal admission is proved only by a later fresh process
after exact terminal, current, fence and release validation.

## Qualification boundary

Hosted Linux Docker executes this matrix and the independent cleanup census.
It cannot establish physical LAN reachability. Acceptance still requires a
real second device to reach every roster application before and after rebind,
plus redeploy, restart, same-NIC address drift, old interface removal and return,
stale-address withdrawal and the app-owned external database create/read path.
Namespace, loopback and host probes remain useful diagnostics but do not replace
that gate.
