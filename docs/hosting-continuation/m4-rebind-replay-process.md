# M4 protected-stage replay in a fresh process

Status: local protocol and source checks passed on 25 October 2026;
hosted Docker acceptance remains pending. Branch
`feature/hosting-m4-rebind-replay-process` is based on draft PR #227 at
`bb926095e9e373e78ceb0adcee8baa22c1f488d8`.

## Purpose

Several live rebind tests try to construct another Manager while the parent
process still owns the generated domain for that data root. The registry refuses
that construction. These tests need a separate process to exercise restart
behavior while retaining their original assertions.

The repair uses the existing stopped-stage child-process protocol for data-volume
replay, stage-config replay and adoption after a lost acknowledgment, stage-start
replay and adoption, and final-config replay and adoption after a lost
acknowledgment. Final-config tests also require the shared stage-start replay
before reaching their own replay checks. Production
constructors, registry checks, authority checks and deadlines remain unchanged.

## Process boundary

The parent creates a private, strictly validated manifest and launches one of a
finite set of child tests. Each child selector has one exact success marker.
Unknown or crossed pairs must be rejected before launch. Manifest input cannot
select a test, command or success marker.

The child uses the supported ordinary constructor and the same SQLite state.
Production stage methods must validate the exact protected history and physical
state. Strict test drivers refuse another volume creation, config copy, container
start or container stop and require zero such calls during replay or adoption.

The parent retains ownership of fixture cleanup and all existing SQL, history,
file-byte, physical identity, topology, predecessor, route and request-count
assertions. Lost-ack cases retain their real initial copy and evidence that the
durable receipt did not advance before child adoption.

This work does not reconstruct an E5 process-local retirement or completion
handle. E6 completion recovery remains a separate requirement.

## Local verification

The final package compiled into a retained test binary. Exact discovery selected
the protocol parent and its cancellation helper. Running that binary with
`-test.run=^TestGatewayRebindStoppedStageReplayChildProtocolIsClosed$ -test.count=1 -test.v -test.timeout=10m`
passed one parent and all 26 subcases, with no failures or skips. The run took
0.15 seconds; the timeout remains a harness bound, not a measured runtime.

Controls cover valid and crossed selector/marker pairs, malformed or private-path
manifest violations, duplicate/tainted/extra markers, closed failure codes,
bounded output and cancellation after observing child readiness. Cancellation
requires a nonnil wait result and a waited process state, then checks that no
success marker was emitted. The parent uses bounded writers so `Wait` finishes
its output-copy work before inspection. It does not require a normal exit from
a deliberately killed process or print captured child output on failure.

The eight Docker parents and eight live child selectors were dynamically
discovered before the final protocol corrections. Their declarations are unchanged
in the final compiled source. These names were not executed locally. The separate
cancellation helper is not one of those eight live children.

Scoped `gofmt -d`, `go vet ./internal/generatedingress`,
`pwsh -NoProfile -File scripts/check-generation.ps1` and `git diff --check`
passed. Formatting used byte-equivalent LF copies of the five source files.
Documentation gates are recorded separately in the publication evidence.

Review corrected three meaningful errors before publication: an extra new-family
completion marker could be accepted; stage-start adoption incorrectly compared a
nine-record history with the ten-record result; and cancellation read pipes could
race with `Wait`. The final adoption check requires exactly one append with the
original nine-record byte prefix intact. Initial compile failures, earlier
nonverbose protocol passes and superseded review decisions remain recorded.

| Final evidence | SHA-256 |
| --- | --- |
| Source manifest | `5cd690dd11d2cc7f1d935098f3c5a20a3f7364ff732cac04919b258a47a4fd4c` |
| Execution index | `e111af8357e0b3fd42961089f8cbde2282bfbf4c764aa86a44c37c3efdecbe49` |
| Protocol raw output | `5ca973191c25413318e5a87f51b1a3fddebb0faf601006500d830bb448694009` |
| Retained test binary | `1e0511034e7f02c8fdc6fc90902e594ce3a6a188521b74e8a472e143ced83cbc` |
| Independent root audit | `c1ab0c670594ecef00614bd0ed7b78868e9691fa40f27035f9789ba9f390a15f` |
| Source/security review | `9b5aaf941466bcd08665ca2345295916103f84fa1350391c73a7488e6d763b3a` |
| Final integration review | `f9c383f3294b2df706854745a4a40ff621630511d695206fe29f839588cec5b4` |

## Remaining hosted acceptance

Use the existing Docker jobs to execute these parents, rejecting skips and
retaining the resource-residue gate:

- `TestLiveGatewayRebindSuccessorStoppedStageContainer`
- `TestLiveGatewayRebindSuccessorDataVolumeStage`
- `TestLiveGatewayRebindSuccessorStageConfigCopy`
- `TestLiveGatewayRebindSuccessorStageConfigCopyLostAcknowledgmentAdopts`
- `TestLiveGatewayRebindSuccessorStageStart`
- `TestLiveGatewayRebindSuccessorStageStartLostAcknowledgmentAdopts`
- `TestLiveGatewayRebindSuccessorFinalConfigCopy`
- `TestLiveGatewayRebindSuccessorFinalConfigCopyLostAcknowledgmentAdopts`

The earlier stopped-stage hosted pass establishes the existing protocol pattern.
It does not establish acceptance of the new replay and adoption cases. Local
Docker and race execution are not claimed.

## Scope and rollback

The source scope is five live test files. No production API, application
database provisioning, runtime secrets or deployment behavior changes.
Reverting this test-only branch restores the earlier fixture orchestration.
It must not require deleting protected history or weakening the registry.
