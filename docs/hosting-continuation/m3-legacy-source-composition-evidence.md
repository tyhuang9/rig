# M3 legacy source composition evidence

## Scope and invariants

Branch: `feature/hosting-m3-legacy-source-composition`, based on runtime commit
`b813eff283a2590cbdabd76fd1b9044ef36d0a6f`.

Purpose: verify that a retained legacy-format rebind can be the source of a
typed rebind, including rollback and fresh-Manager replay. This unit changes
tests and their Docker boundary simulation. Production effect factories remain
closed. The independent startup-ordering branch is not integrated here.

The initial legacy lifecycle uses existing protected-history fixtures and the
real SQLite repository's guarded transitions. It is historical seed preparation,
not evidence of concrete Docker execution for that initial lifecycle. The legacy
SQL compatibility projection uses its retained predecessor journal digest; it
does not invent a typed predecessor checkpoint. An ordinary redeploy aligns the
historical fixture's current route and SQL runtime through the existing bounded
physical seed driver. Only its mutable current bundle may change; legacy intent,
progress, receipt, committed SQL history and transfers must remain unchanged.

After seed preparation, one retained legacy executor supplies actual simulated
container, volume, network, endpoint and config observations. It remains the same
object through the subsequent concrete typed driver journey and fresh Manager.
It is represented separately from typed intents. Shared inventory and probes
observe all retained gateways, and stale application endpoint IDs are refused.

Acceptance assertions cover:

- Canonical legacy terminal selection, with substituted or missing authority
  refused.
- Concrete typed commit and completion-write failure followed by rollback.
- Exact old-container stop, and exact restart only on rollback; no other legacy
  effects or legacy resource removal.
- Unchanged protected legacy files, current bundle, committed SQL history and
  raw allocation/access/grant/profile rows.
- Exact extension of the prior transfer chain on commit; original authority and
  transfer chain retained on rollback.
- Fresh-Manager replay against the same physical backend without effects or SQL
  and protected-file changes. Replay must select the exact typed receipt after
  commit and exact original legacy receipt after rollback.

## Baseline and verification

The pre-existing attempt-view fixture supplied only `Receipt`, although the
current adapter requires canonical `Terminal` for a rebound source. The prior
prerequisite run failed that test in 17.911s (log SHA-256
`CF44F56653AD28E54916C22A1EB398CB30288E0471A352AA162EA4E5A8929C0D`).
This unit repairs the fixture and tests rejection of substituted canonical
receipt authority and missing terminal selection. Production validation is
unchanged.

Initial narrow checks, before final backend composition:

| Selection | Result | Package time | Log SHA-256 |
| --- | --- | --- | --- |
| `^TestGatewayRebindAttemptView` | 3 top-level tests passed, exit 0 | 17.679s | `757349EB2C9106A9D6C94EBC99DD085C07F301860484E081505C1F4A04200D79` |
| `^TestGatewayRebindLegacyCompositionSeedRetainsCanonicalAuthority$` | 1 test passed, exit 0 | 22.686s | `1D01488FA62A1DA5F25C8120754EED752B17D7AF780C6E7070E52CD2B9DFB9CB` |
| `^TestGatewayRebindLegacyCompositionCurrentCensus$` after image correction | 1 test passed, exit 0 | 20.715s | `C19F915E212772159599762C303F40B537C85521009D97D108F9373F2724B5F0` |

Checks use Go 1.26.0 on Windows, with `-mod=readonly -buildvcs=false -p=1
-count=1 -json`. Logs are persisted per line outside the checkout under
`../temp/m3-legacy-source-20261007-*.jsonl`, with separate exit-code records.

The first concrete current-census run failed in 20.720s before any effects. Its
log SHA-256 is `23D237DA55E18E7380222D027AE82759AA94F972B1F6D3600932FF9D28002779`.
Source review found the test backend omitted the known-absent legacy stage
container required by current attestation. Review also identified pinned-image
resolution, exact known-name reads, and removal of stale native application
networks from the legacy-mode global census as fixture corrections. Production
checks are retained; this failed run is not counted as acceptance.

The next census (20.497s) and diagnostic census (20.550s) still refused before
effects. The diagnostic confirmed SQL selection succeeded but no read reached
the legacy executor. The composed historical fixtures retain different image
content IDs (`a…a` for native, `b…b` for legacy); requiring those IDs to equal
was an erroneous simulation restriction. Exact content-ID reads now retain each
image object, while the exact pinned reference selects the native image used by
the existing typed creation fixture. Unknown references remain refused. No
protected receipt, image binding or old container is rewritten to fit the model.
Diagnostic log fingerprints:

- Corrected census: `7A0831CCB921CDAA80B3AD122ABA474B2CE04283786A864009D30A297F4FB276`.
- Diagnostic census: `E656B624E6238128167DAB634413D6E7E473D64B32B52292D53950C05467A7AD`.

The first aggregate legacy acceptance run passed the five preliminary tests but
both handover cases refused after typed progress 12, before stopping legacy or
creating the final successor. Package exit 1, 391.372s; log SHA-256
`709238605A00845D863A80907E42E209E1E83EFB70C5999ED2F00B758449FBB8`.
The fixture had advertised only the new host address, omitting the retained
current address. The production handover proof correctly rejected a running
predecessor with routes but no corresponding host address. The fixture now adds
the exact current interface/address/prefix before admission and canonicalization;
it does not change an admitted intent or weaken runtime proof validation.
This failed package is not counted as passing acceptance.

Final-source `go vet -mod=readonly -buildvcs=false -p=1
./internal/generatedingress` and `go build -mod=readonly -buildvcs=false -p=1
./...` both passed, exit 0. Read-only gofmt and whitespace checks passed.

The existing second-generation gate passed commit (391.31s) and rollback
(512.99s), with the package passing in 904.610s, exit 0 and no skips. Exact
discovery of the top-level test and both subtests was checked, along with the
final package event and exit file. The process was reaped. Log SHA-256:
`6213BAD3E5784B79AEE834DD84A79696A3A3253B2072FD79E19088803780B9A1`.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=40m -json ./internal/generatedingress -run '^TestGatewayRebindConcreteCompositionSecondGeneration$'
```

This regression started before the final host-census correction in the new
legacy fixture. Its selected tests and reachable backend source remained
unchanged throughout; it does not exercise the corrected legacy helper. The
backend SHA-256 was and remains
`67F6CCC2C800A825A66CA3A761F2206B80DB514A5B78DE8A3598EBCAAD3A85C4`.

Final legacy acceptance passed all six expected top-level tests and both
commit/rollback subtests, exit 0 with no failures or skips. The concrete commit
took 277.65s, rollback 383.77s, and the package 734.354s. Exact test discovery,
the final package event and the exit file were verified. The process was reaped.
Log SHA-256:
`B6C32F6EDD7043AE1095411C5D6F92AA18D2AC07F7F169AB17C7C08077AEBA17`.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=30m -json ./internal/generatedingress -run '^TestGatewayRebind(ConcreteCompositionLegacySource|LegacyComposition|AttemptView)'
```

Together the final accepted runs establish seven top-level tests and four
subtests, with zero failures or skips. Earlier failed runs remain above as
diagnostic evidence and are not counted as passing packages.

The final legacy acceptance source remained fixed during execution; file
SHA-256 fingerprints were rechecked afterward:

| File | SHA-256 |
| --- | --- |
| `gateway_rebind_attempt_test.go` | `F82EF5055358E3DDADD85670DF15962192FC57CD0443B989EF4542F71BB03211` |
| `gateway_rebind_multigeneration_backend_test.go` | `67F6CCC2C800A825A66CA3A761F2206B80DB514A5B78DE8A3598EBCAAD3A85C4` |
| `gateway_rebind_legacy_composition_test.go` | `46FF345516DD29803C3327C21E0F2A188394924EE525F3CCCC4DA545F0A1122D` |

Source/code-quality and independent security review accepted the corrected
split-image backend, exact-authority assertions and host-census correction.
The orchestrator reviewed the aggregate scope; the backend worker owned only
the shared test backend, while the root owned the seed/journey tests and all
executable checks. Review alone was not treated as executable acceptance.

## Next dependency step

Startup-ordering commit `6924a32d8efb0725e76ec5d00f59081328ebe121` and this branch
are independent sibling units based on runtime commit `b813eff`. Integrating
both into the runtime branch requires separate explicit local integration
approval under the standing restriction. After integration, verify startup
dispatch, same-Manager inspection, and mixed-format current selection on the
combined source. Production factory enablement and real Docker acceptance
remain later gates.

## Limits

This is real SQLite and protected-file integration with simulated Docker commands,
config and probes. It does not establish live Docker networking, abrupt process
death, Linux race behavior, frontend behavior or the full repository suite.
The independent historical fixtures do not establish real Docker digest
provenance for their seeded images; the command boundary models explicit retained
content identities and one pinned-reference selection for subsequent creation.
Hosted Docker CI and the subsequent startup/runtime integration remain separate
acceptance gates. No controller restart, deployment, database provisioning,
publication or GitHub merge is part of this unit. External databases remain
application-owned and configured through scoped runtime secrets.

No production behavior changes here, so reverting these test changes is the
rollback. Retained protected history and runtime credentials must not be deleted.
