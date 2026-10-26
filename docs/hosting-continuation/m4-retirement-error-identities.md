# M4 retirement error identities

## Purpose and evidence

Distinguish existing error identities when previous-runtime retirement refuses
completion. This follow-up starts from [PR #239](https://github.com/tyhuang9/rig/pull/239)
at commit 0e9a93c379e91fefaae18023f129bd71ef8ffb99.

The [PR #238 TLS journey](https://github.com/tyhuang9/rig/actions/runs/40258708583/job/121366115011)
reached healthy replacement but paused with route_reconciliation_required.
The failed child's retained diagnostic was previous_retirement/unknown.
The parent failed after 227.27 seconds at synthetic merge
ebbb6409474d479a9a0518be7be5d68159716572.
Raw log SHA-256:
6cba1cea403b52630564bd17e6e134c6b5dad070f063bbd48e0ac430e5c3f791.

The producer intentionally emitted unknown, and the hostd parser accepted
that exact pair. This observation identifies a nonnil retirement handle whose
completion returned an unclassified error. It does not identify the internal
completion checkpoint or establish the journey's first failure.

The separate process-kill job emitted retirement_unavailable. That result
does not establish the cause of the TLS failure.

## Classification contract

Preserve the entire existing classifier, including the sealed-cause branch's
early unknown result. Consider new identities only at the old final fallback.
Use existing error identity predicates; do not inspect error messages or add
wrappers, callbacks or lifecycle state.

The new fixed labels describe invalid claim or configuration, unavailable
scope, stale/invalid/unresolved authority, lost identity, invalid stage,
positive fence, unavailable storage and an unresolved route. They are allowed
only with previous_retirement. Existing deployment-execution cancellation
uses the existing canceled label.

After existing classifications and cancellation, count distinct approved
non-storage identities, including route-unresolved:

- One identity supplies its fixed label, including when joined with storage
  unavailability.
- More than one distinct identity produces unknown, independent of join
  order. Repeated occurrences of the same identity count once.
- With no non-storage match, storage unavailability has its fixed label.
- Unmatched errors remain unknown.

These labels describe matched error identities. A sentinel can originate in
several completion operations, so it does not establish a retirement, handoff,
transaction or readback checkpoint.

## Invariants and verification

Completion, authority checks, effect order, locking, transactions, ambiguous
commit readback, error identity, retries and cleanup are unchanged. Diagnostic
output remains limited to a fixed stage/cause pair; raw errors, identifiers,
paths, SQL, credentials and Docker output are excluded.

Focused controls cover exact identity mappings, preserved precedence, joined
ambiguity, duplicates, wrong-stage rejection and private text. The existing
authenticated fixture supplies a real nonnil retirement handle; its existing
completion test seam verifies the unchanged pause and effect behavior.
Hostd parser controls cover the matching finite pairs and malformed records.

Fresh compilation, exact discovery, relevant controls, formatting, scoped vet,
canonical generation, diff and documentation checks must pass before delivery.
The next hosted TLS run must provide the actual error identity. Local
classification tests do not prove route completion, cold startup, E6 recovery,
or full M4 acceptance.

## Rollback

Revert the producer vocabulary and matching hostd parser together. Preserve
immutable history and recorded evidence. This diagnostic change introduces no
schema migration, runtime authority or database provisioning.
