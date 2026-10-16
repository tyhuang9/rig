# M3 native batch fixture authority histories

Date: 2026-10-16

## Fixture contract

The historical native batch fixture accepts the two complete committed gateway
upgrade histories produced by supported repository transitions:

- a public synchronous commit: `prepared` at sequence 1, then `committed` at
  sequence 2; and
- a staged commit: `prepared`, `serving`, then `committed` at sequences 1
  through 3.

The fixture does not accept an arbitrary history length. It requires contiguous
sequence numbers, the initial prepared event at the claim approval time,
nondecreasing event times, the supported optional serving event, and the final
committed event at the claim update time. Upgrade events must not carry gateway
operation or protected-state proof fields. The repository startup snapshot also
validates the retained event chain before the extractor can reconstruct it.

All existing historical authority guards remain in force: one administrator,
one machine, two distinct applications, allocations, grants, disable intents,
and ports; active foreign keys and migration triggers; and no retained session,
job, deployment, runtime component, audit, or rebind rows. Migration 029 must
still create exactly two distinct prepared sequence-1 disable claims from the
two immutable intents, with the committed native upgrade as the current gateway
source and no foreign claim.

## Regression coverage

`TestLegacyNativeBatchPublicCommitAuthorityExtractorRoundTrip` writes the
two-event history into a guarded through-028 SQLite source, migrates that source,
reads it through the production startup snapshot and fixture extractor, verifies
the immutable values, reconstructs a second guarded through-028 database, and
runs the production migration runner through migration 029. The original
three-event roundtrip remains covered separately.

The regression arranges the legal public-commit event shape through the
historical fixture's guarded SQL replay; it does not invoke the public controller
to create that source. Active foreign keys and transition triggers enforce the
stored shape, and the production startup snapshot validates the complete chain
before extraction. The hosted case remains the proof that the completed public
controller and runtime path supplies the live source authority.

Focused negative controls reject a missing prepared event, approval-time
mismatch, sequence gap or duplicate, truncated history, unsupported middle or
final state, malformed or backwards time, final-time mismatch, and either
prohibited proof field.

These local controls establish the SQLite event-history contract, extraction,
historical reconstruction, and migration result. The hosted
`pending-lan-batch-restart` case remains responsible for the live public source,
physical two-port quarantine, crash boundary, protected batch identity, and
fresh-process recovery.
