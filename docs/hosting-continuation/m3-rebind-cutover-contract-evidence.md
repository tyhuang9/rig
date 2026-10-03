# M3 LAN gateway rebind cutover contract evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-quiescence-census` at `24bbffe`

Branch: `feature/hosting-m3-rebind-cutover-contract` (local and unpublished)

## Scope

This branch records the proposed durable state and failure contract for an M3
LAN gateway rebind. It changes no executable code, SQLite migration, protected
artifact, Docker resource, URL, allocation, or active fence. The contract is a
prerequisite to a writer design; it is not evidence that a rebind can run.

## Verification

The base `24bbffe` passed the repository-wide Go suite and vet. This
documentation branch passed `git diff --check`, and every `internal/...` source
path cited by the contract resolves in the checkout. No runtime test is
claimed for a docs-only change.

Initial independent security and manual reviews found contradictions in
post-database-commit rollback, old-listener stop proof, transfer-aware normal
authorization, preclaim attestation order, fixed Docker resource names, and
postterminal lock-release behavior. The contract was revised to specify an
immutable-event guard, exact-owned predecessor stop/no-old-bind proof,
validated effective-profile transfer chains, distinct preclaim and postclaim
attestation, generation-scoped successor resources, and process fail-stop after
a terminal SQL commit. Re-review found three additional contract precision
issues, also corrected: atomic successor head/transfer/event commit, a short
identity-bound Docker hostname, and migration-034 replacement of migration
033's unique `(operation_id, state)` event constraint for recovery replay.
Final integration also identified and resolved typed predecessor lineage,
migration-026 profile pin exceptions, and nested-lock risk when reusing the
post-insert attestor. The aggregate staged diff contains only this contract
and evidence note; `git diff --cached --check` passed.

## Open acceptance gates

Migration 034, a claim writer, protected successor and terminal receipt,
physical cutover/rollback, LAN-08 old-NIC/DHCP handling, hosted Docker CI,
Linux race, and a physical second-device proof remain open. No publication,
merge, or deployment is authorized by this contract.
