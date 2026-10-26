# M4 runtime fixture ownership and replay

## Observed failure and source diagnosis

[PR #244's runtime commit/replay job](https://github.com/tyhuang9/rig/actions/runs/40280090070/job/121431275593)
failed with the fixed message `open typed runtime manager` after 84.39
seconds. The checked-out synthetic commit was
`809041a85571094ee2e92c42f9cb7da07236ec20`, with the published head's tree.
Raw log SHA-256:
`a5bf42c1dde1ef7ec05b19acf3cb12df2b89b94801eccf66182f579cd4a5137d`.
That message does not reveal the underlying construction diagnostic.

The relevant fixture and ownership code is unchanged in PR #245 at
`71c4640e8a84e3b4288d2d39bac06339a804aa2a`. The source fixture constructs
a `GeneratedDomain` and retains its gateway Manager. The typed runtime
fixture then calls public `New` again against the same controller root,
database and Docker namespace. With those identities intact, the existing
domain reservation makes this second Manager conflict with the first.
That is a source-supported explanation for the observed failure, rather
than a diagnostic extracted from the hosted log.

The same test later calls `New` again in the original process for its
restart/replay check. The process still owns the original domain. A valid
restart check needs a fresh process while preserving the existing replay
assertions.

## Correction contract

- Construct the diagnostic runner before creating the source domain and
  pass it unchanged through private fixture constructors.
- Reuse the source domain's exact gateway Manager, checking pointer
  identity. Preserve its sealed repository callbacks, runner, options and
  driver fields. Use the existing request-scoped commit-driver seam for
  diagnostic wrappers.
- Keep the existing fixture constructors as compatibility wrappers.
- Run commit and rollback replay in genuine child processes using the
  established bounded manifest and finite selector/marker protocol.
- Retain the existing SQL, protected-history, terminal-receipt, selected
  authority, physical attestation, route and zero-replay-effect assertions.
- Preserve the single-owner registry restriction, including a deliberate
  conflicting second-Manager refusal control. Never reset ownership or
  mutate a published Manager to make the test pass.

The child protocol must reject crossed or arbitrary selectors, malformed
or oversized manifests, duplicate or tainted markers, output overflow,
failed or skipped parent tests, and timeout/cancellation without reaping.
It must not surface private paths, configuration, IDs, SQL or raw Docker
output. Existing stopped-stage child callers keep their behavior.

This is a test-fixture change. It does not modify production construction,
runtime authority, public APIs, database provisioning or deployment.
External databases remain application-owned. The separate retained-V2
production repair in PR #245 still requires hosted acceptance.

## Acceptance status

The final frozen test source passed focused Windows verification on
2026-10-26 with Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false` and `CGO_ENABLED=0`.
The source manifest SHA-256 is
`9a22e000b94126f7d2ba3cb4c5d78184d4f2d7850f43075e585158a417f3caed`.
The freshly compiled binary SHA-256 is
`2de3431908df534bbe96618325a8488fe4d277eda3f21d64e07bb3e9ed7b1f1b`.

Actual discovery found 1,298 parent tests, including both new replay-child
parents. Four selected parent groups and 48 child cases passed, for 52
unique passes with zero failures or skips:

- `TestGatewayRebindStoppedStageReplayChildProtocolIsClosed` (0.83 s)
- `TestGatewayRebindRuntimeLivePreconditions`
- `TestLiveGatewayRebindRuntimeCoordinatorRetainsFirstCurrentAttestationCheckpoint`
- `TestGatewayRebindRuntimeReplayCommandAudit`

The latter three groups each reported less than 0.01 seconds. Tests used
anchored selectors, `-test.v`, `-test.count=1` and bounded timeouts.
Formatting, scoped `go vet ./internal/generatedingress`, canonical
generation, diff and unchanged-source checks also passed.

The completed-owner control now executes the actual same-process conflict:
ordinary `New` returns no Manager and `DiagnosticRouteUnresolved`, with
zero additional commands. The original construction and gateway pointers,
SQL snapshot, protected history and route state remain unchanged. The
first control attempt expected a daemon inspection, but the root ownership
conflict refuses before that read. That failed oracle run is preserved;
the final control requires the observed, source-supported zero-command
behavior. No production guard was changed.

Protocol controls cover both added finite selector/marker pairs alongside
the existing private-manifest, invalid-pair, marker, overflow and
cancellation checks. The new live fixture has explicit owned-Manager and
construction-runner identity assertions, but their real Docker execution
remains pending.

These local controls qualify the ownership contract and child protocol;
they do not prove the complete Docker journeys or identify the underlying
diagnostic of the earlier hosted fixed-message failure. No local Docker,
Linux race or manual UI run was performed. Hosted jobs must establish both
full commit and rollback journeys, including genuine process replay and
the preserved SQL/history/physical assertions. Full M4 acceptance remains
open.

## Rollback

Revert this fixture change through the PR workflow to restore the prior
tests and their observed construction failure. No production data,
schema, runtime resource or immutable history migration is introduced.
