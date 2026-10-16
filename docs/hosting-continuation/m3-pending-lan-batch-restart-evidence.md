# M3 pending LAN batch restart coverage

Date: 2026-10-16

## Requirement retained

The supported rebind boundary requires the named `pending-lan-batch-restart`
case to preserve exact-operation precedence for more than one pending LAN
operation across a fresh process. The case still requires two distinct durable
claims at the crash, one immutable protected queue, physical withdrawal before
the crash, and the same protected head after restart. Sequential single-claim
recoveries do not satisfy it.

Ordinary current admission deliberately serializes LAN mutations. A second
unresolved grant or disable is rejected and cannot create another current
claim. Migration 029 is the supported source of a multi-claim historical
queue: every valid migration-027 disable intent is backfilled as its own
prepared claim before the current single-unresolved fence is installed.

## Live fixture route

The live fixture first creates two applications, their deployments, a native
v2 gateway upgrade, and two committed LAN grants through the current public
controller and runtime paths. It then closes SQLite and replaces only the
database with a through-028 schema. Foreign keys and migration triggers remain
enabled.

The historical database reconstructs a fixed authority allowlist from the
validated public fixture:

- the administrator, shared machine, and two unarchived applications;
- the current gateway profile and committed native upgrade history;
- each allocation and access revision, replayed through reserved, owned, and
  active transitions;
- each committed grant identity and event history, including the real native
  gateway operation and protected-state digest; and
- two new migration-027 disable intents, one for each exact allocation.

It does not copy sessions, jobs, deployments, generated runtime deployments or
components, audit rows, rebind rows, or selected-current proof. Application
insertion creates the schema-required generation-zero runtime-head placeholder
for each app; both must remain empty. The production migration runner then
creates exactly two migration-029 prepared claims at sequence 1 and no foreign
claim. A fresh session is created only after migration reaches the current
schema.

The native `routes-v2.bundle` and Docker topology are never reconstructed. The
bundle is the purpose-bound artifact previously written and read back by the
real gateway upgrade and grant paths.

A no-Docker roundtrip control exercises the same authority extractor against a
guarded through-028 native authority migrated to the current schema. It proves
that the exact query and event-history scan preserve every immutable source
value, then uses the extracted rows to seed a second guarded through-028
database and verifies the real migration-029 two-claim result. Census controls
reject a wrong current source and foreign or incomplete grant sets. The hosted
case remains responsible for proving extraction from the completed public
fixture and the physical Docker state.

## Crash and restart proof

Startup uses `QuarantineGatewayV2LANAccessRecoveryBatch`. It writes the
immutable native batch, applies the projection without the two unsafe LAN
bindings, and proves the unsafe ports. The existing test runner delegates the
real Caddy `mv .../active.next.json` command before it emits the
`current_effect_ack_lost` boundary and blocks. At that boundary the case
requires both exact LAN ports to return `404`.

The harness reads `routes-v2.bundle` with purpose
`hostd/generated-ingress/routes/v2` and retains a bounded identity: plaintext
digest, native upgrade operation, head, ordered item identities, count, and any
legacy-pending digest. It requires `Head == 0`, `Count == 2`, two distinct
disable operations, exact allocation and source-grant identities, and the same
production order as the SQL claims. After killing and reaping the first child,
SQLite must still contain the same two prepared sequence-1 claims. A fresh
child must report recovery-only `lan_disable` for that exact head, and a second
purpose-bound read must match the complete precrash identity.

## Coverage split

Selected-current `GatewayCurrentRecoveryLANBatch` behavior remains covered by
the focused generated-ingress unit suites. The named live case covers the
historically reachable native fallback in
`gateway_v2_lan_recovery_batch.go:QuarantineGatewayV2LANAccessRecoveryBatch`.
The manifest wording and anchor identify that actual path.

The local SQLite and harness controls establish schema constraints, migration
identity, parser boundaries, and compilation. They do not establish Docker
quarantine timing or fresh-host recovery. The named Docker case remains a
hosted acceptance check.
