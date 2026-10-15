# Supported controller rebind crash boundary manifest

Status: candidate source contract, 2026-10-15. This document defines the
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
   heads and grants, serving or withdrawal, fence and latch state. Separate
   isolation scenarios require the seeded ordinary job to stay queued during
   recovery, and enabled auto-deploy/relay startup to remain absent through
   reconciliation in the same restricted process. A fresh normal child must
   start both enabled components and advance their database sentinels.
9. The parent removes and rechecks exact containers, networks, volumes, images,
   builders, adapters and child processes even when setup, crash or restart
   fails.

The executable source of truth for exact IDs and source anchors is
`cmd/hostd/gateway_rebind_live_manifest_test.go`. Its untagged control test
rejects missing, duplicate, unknown or skipped manifest entries.

## Ordered durable writes and effects

These outcomes describe this healthy, exactly owned fixture after restart.
Early prepared/progress states have no fixed terminal disposition: recovery
revalidates retained authority and physical evidence, then tries forward
completion. A failure may require compensation. A recorded abort, commit
receipt or immutable database-commit event constrains that choice; this matrix
does not grant permission to override it. The two explicit rollback boundaries
retain their deliberately injected forward failure and must abort.

| ID | Required proven boundary | Expected healthy-fixture result |
| --- | --- | --- |
| `a0-sql-claim` | Prepared SQL claim exists while checkpoint, intent, progress and terminal are absent. | Commit after fresh exact no-effect proof and safe prepared-state reconstruction. |
| `p0-predecessor-checkpoint` | Exact predecessor checkpoint is durable and readable. | Commit after fresh proof and safe reconstruction of the remaining prepared prefix. |
| `p1-protected-intent` | Protected intent is digest-bound to A0, checkpoint, roster and runtime heads. | Commit after fresh proof and safe progress initialization. |
| `p01-successor-intent` | Protected progress sequence 1 records successor intent. | Commit after verified forward recovery. |
| `p02-image-observed` | Sequence 2 binds the immutable image and stage plan. | Commit after verified forward recovery. |
| `p03-network-bound` | Sequence 3 binds the successor network and ownership. | Commit after verified forward recovery. |
| `p04-config-volume-bound` | Sequence 4 binds the config volume. | Commit after verified forward recovery. |
| `p05-data-volume-bound` | Sequence 5 binds the data volume. | Commit after verified forward recovery. |
| `p06-stage-container-bound` | Sequence 6 binds the stopped stage container. | Commit after verified forward recovery. |
| `p07-stage-config-intent` | Sequence 7 precedes the stage config copy. | Commit after verified forward recovery. |
| `p08-stage-config-copied` | Sequence 8 attests copied stage config bytes. | Commit after verified forward recovery. |
| `p09-stage-start-intent` | Sequence 9 precedes the exact stage start. | Commit after verified forward recovery. |
| `p10-stage-serving` | Sequence 10 binds the serving stage endpoint. | Commit after verified forward recovery. |
| `p11-final-config-intent` | Sequence 11 precedes the final config copy. | Commit after verified forward recovery. |
| `p12-final-config-copied` | Sequence 12 attests copied final config bytes. | Commit after verified forward recovery. |
| `p13-final-handover-intent` | Sequence 13 binds predecessor observation and application networks. | Commit after verified forward recovery. |
| `p14-final-container-bound` | Sequence 14 binds the final successor container. | Commit after verified forward recovery. |
| `p15-cutover-intent` | Sequence 15 binds the exact cutover observation. | Commit after verified forward recovery. |
| `p16-successor-serving` | Sequence 16 proves successor serving after cutover. | Commit after fresh physical serving and retained-authority proof. |
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

All ten healthy lost-acknowledgment cases require commit. The typed v2 forward
driver can bind an already observed network, volume or stage container only
after exact ownership and persisted-prefix checks; it must not repeat creation
or accept an arbitrary resource. The later effects likewise require fresh exact
proof before adoption. These are strict fixture outcomes, not permission to
accept either terminal result. Forced rollback retains its separately recorded
cleanup authority and abort decision.

This adoption applies only to the exact expected next resource whose durable
binding acknowledgment was interrupted. The controller contract's refusal for
unbound or foreign resources still applies when the full expected identity,
ownership, inventory or retained-prefix proof is unavailable or inconsistent.
No resource gains authority merely because it exists or has a similar name.

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
alive. Its subsequent healthy startup recovery must commit; the A0 claim has
no fixed abort receipt. The current-role case obtains both recovery digests as an administrator,
then revokes that live session before submitting reconcile with those exact
digests; the denied request must leave the operation active and unchanged.

The recovery process must retain its restricted routes after a terminal result.
The `recovery-worker-isolation` scenario requires an already queued ordinary job
to remain untouched. The additional `recovery-background-isolation` scenario
enables GitHub and relay configuration, records passive pre-launch observations,
and seeds an expired auto-deploy lease and a disconnected relay session epoch in
the disposable database. All observations and retained sentinel values must stay
unchanged through readiness, authenticated requests and completed reconciliation
in the same recovery process.

A fresh normal child must then record exactly one start per enabled component,
clear the expired lease and advance the relay epoch. These positive controls
establish that startup would be observable. They run before deployment preflight
and relay credential recovery. The fixture uses no active/pending relay key or
credential file, restricts relay origin to loopback HTTPS and denies GitHub HTTP
transport in the child. The private observer cannot authorize or replace a
factory or runtime action. Normal admission still follows exact terminal,
current, fence and release validation. The closed manifest now requires 33 crash
boundaries and 30 scenarios; hosted execution is required to establish acceptance.

## Qualification boundary

Hosted Linux Docker executes this matrix and the independent cleanup census.
It cannot establish physical LAN reachability. Acceptance still requires a
real second device to reach every roster application before and after rebind,
plus redeploy, restart, same-NIC address drift, old interface removal and return,
stale-address withdrawal and the app-owned external database create/read path.
Namespace, loopback and host probes remain useful diagnostics but do not replace
that gate.
