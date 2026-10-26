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

## Original ownership/replay verification

The original published test source at
`7680cff3ebfb8602b67178fef9df39bca9497586` passed focused Windows verification on
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

## Gateway V2 restart follow-up

[PR #245's Gateway V2 Docker job](https://github.com/tyhuang9/rig/actions/runs/40302965729/job/121505057206)
failed its commit/restart and bind-conflict/rollback tests after 55.16 and
38.87 seconds, respectively. Both reported fixed restart-manager errors;
the log does not expose the underlying diagnostic. Raw log SHA-256:
`aa2922632f457f4e249595d809b72fc9ce2e13d5ee99984b811821867c33bc83`.

Both test bodies retain the fixture's original domain owner and then call
ordinary `New` again in that process. The follow-up applies the same
ownership correction to these two restart checks: use separate processes
with two exact selector/marker pairs in the existing bounded protocol.
Keep the manifest private, use the actual upgrade-journal operation ID,
and construct one Manager in each child.

The child must derive the unique application and current route from
authenticated protected history and the corresponding SQL source. It
checks the pair-specific committed or rolled-back terminal, observes the
reconstructed route, and preserves SQL/history with no mutation or
capacity commands. Missing, crossed, ambiguous or inconsistent sources
must refuse. The parent retains its fixed application, exact route and
HTTP/listener assertions, plus physical-resource and cleanup checks.

This follow-up changes two test files only. Production ownership and
restart rules stay intact. On 2026-10-26, a fresh ordinary Go 1.26.7
compile and exact discovery passed. The two live parents, two new child
parents and two local control parents were each discovered exactly once.
The executed controls were `TestGatewayRebindRuntimeReplayCommandAudit`
and `TestGatewayRebindStoppedStageReplayChildProtocolIsClosed`: two
parents and 43 child cases, 45 unique passes with no failures or skips.
They ran with an anchored selector, `-test.count=1`, `-test.v` and
`-test.timeout=10m`. Formatting, scoped vet and canonical generation
checks also passed.

The Gateway V2 follow-up, published at
`2c94e8193ef584b4ead40f1bac12c5c3359f3ce8`, has source manifest SHA-256
`b0836830ed71e539b66fc8d874917cd190b55682531088e4709ef1ef5fdf0288`;
its binary SHA-256 is
`63f63303b2bd22ac27daf0cdead747b0432d4393c485267e5a0e11b17a615608`.
Source/security review found no blocking issue. SQL runtime heads encode
application identity and slot; protected route state supplies endpoints.
The child checks their supported relationships and preserves each full
snapshot without claiming SQL contains endpoint topology.

The live parents and children were discovered, not executed locally.
Both real Docker journeys remain pending hosted verification. The earlier
52 passes above describe the original published source and are not added
to the follow-up's 45-pass count.

## Cross-store proposal replay follow-up

[PR #245's cross-store proposal job](https://github.com/tyhuang9/rig/actions/runs/40302965729/job/121505057798)
failed after 64.70 seconds with the fixed message `open real manager with
the repository rebind fence`. Its raw log SHA-256 is
`f2c7f75c4f994c457cafed4221c8dcfa78752bdd6969dfd284b7b33530d62b9a`.
The underlying diagnostic is not exposed by that output.

The proposal test is unchanged at `2c94e819`. It constructs a source
fixture that already owns a repository-bound gateway Manager, then calls
`New` against the same namespace and replaces its Manager. It later calls
`New` again in the same process for a purported fresh replay. Both paths
conflict with the retained domain ownership contract. Removing only the
first construction would leave an invalid restart check.

The bounded follow-up uses the fixture's original owned Manager for
parent inspection and a genuine child process for proposal replay. The
new child mode uses the existing private manifest and input validator,
an exact selector, bounded output, closed markers and process cleanup.
Existing prepared-claim crash modes retain their behavior.

The child must reconstruct the proposal from validated expected input,
create one fresh Manager and compare the complete inspection. Both
processes preserve SQL authority, runtime heads, application access,
protected routes/history, gateway state/journal and source Docker
identity. The original LAN/loopback roster, provenance, open-fence and
absence-of-intent assertions remain required. Proposal inspection must
not forward an application request or dispatch a mutation command.

On 2026-10-26, fresh pinned Windows Go 1.26.7 compilation and exact
four-selector discovery passed. The executed selection was
`^TestLiveGatewayRebindCrossStoreProposalReplayProtocolIsClosed$`, with
`-test.count=1 -test.v -test.timeout=5m`. It produced 26 unique passes:
one parent and 25 nested tests, with no failures or skips. These cover
private manifest round-trip, malformed and tainted input refusal, exact
child output, and cancellation/reaping. Formatting, scoped vet and the
canonical generation check passed; the source stayed unchanged.

The final source manifest SHA-256 is
`f089eca46a15adb6dfdb45fbfb9a044078c91ef433dd3eee5bad887de7359416`;
the test binary SHA-256 is
`cd14e11db90881f86834dc767f2fca7c0b7b1452f1b8f401be721e25fdc2e5fb`.
Source/security review found no blocking issue. Earlier attempts are
retained: one stopped at an incomplete fixture before the protocol;
another exposed validation before reconstruction of the derived roster
digest omitted by JSON. The final decoder reconstructs that digest and
then performs full validation. Exact inspection equality is retained,
with no timestamp normalization or approval reconstruction.

Actual hosted proposal replay remains required. Local protocol checks
cannot establish fresh Manager construction or Docker observations and
do not newly execute completed-domain ownership refusal. The earlier
52-pass and 45-pass results belong to their respective published source
versions and are not added to this follow-up's 26-pass count.

## Hosted Gateway V2 outcome and command audit

On published `2c94e8193ef584b4ead40f1bac12c5c3359f3ce8`,
[the hosted Gateway V2 job](https://github.com/tyhuang9/rig/actions/runs/40309622919/job/121527253873)
executed the genuine commit/restart parent successfully in 58.50 seconds.
The passive Docker and public predecessor parents also passed in 47.89
and 56.79 seconds. The bind-conflict rollback parent failed after 36.22
seconds at the child's fixed `history` stage. The job and complete
rollback journey remain unaccepted.

The exact captured checkout was
`66fcb69f7baaf2ade7ddc8e4356bd75e72718cb1`; raw log SHA-256:
`fc77a0e8f550dac2522e791a92b8013757d40fbcb488cd9b02fa636b9d64cd92`.

Source inspection identifies a false positive in the test's command
audit. Rollback observes the original legacy gateway, whose config read
uses this exact argument sequence:

```text
container exec <container-id> curl --silent --show-error --fail --max-time 10 http://127.0.0.1:2019/config/
```

The audit currently recognizes the newer config-read sequence but marks
this older sequence as an effect. That alone fails the combined history
and zero-effect assertion after otherwise successful replay. The fixed
stage does not reveal whether another predicate in that assertion also
fails.

The follow-up recognizes only this exact additional read form and adds
its positive case and an extra-token refusal to the existing audit
table. Full history, SQL, effect, physical-resource and cleanup checks
remain required. Production command construction and the child protocol
stay unchanged.

On 2026-10-26, fresh pinned Windows Go 1.26.7 compilation and exact
discovery of `TestGatewayRebindRuntimeReplayCommandAudit` passed. The
anchored selection used `-test.count=1 -test.v -test.timeout=2m` and
produced 19 unique passes: one parent and 18 rows, with no failures or
skips. Gofmt, scoped vet, canonical generation, diff and source-stability
checks passed. Source/security review found no blocking issue.

Source manifest SHA-256:
`88c50dd919cfcd045428e6ba67997e9bb0f32d0ac5dd94279ae66c429db20a15`;
binary SHA-256:
`241f92f730c51e6bb5ee88929220fc069e321257d8f477887c743c3fbc0d0314`.
The local audit proves the exact classification and table refusals.
Hosted rollback must execute again with all its assertions before
acceptance. Earlier test counts describe earlier source versions and
are not added to this follow-up's 19-pass count.

## Rollback

Revert this fixture change through the PR workflow to restore the prior
tests and their observed construction failure. No production data,
schema, runtime resource or immutable history migration is introduced.
