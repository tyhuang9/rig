# M4 retained V1 proof after gateway recovery

## Observed failure

[PR #241's A0 run](https://github.com/tyhuang9/rig/actions/runs/40266730836/job/121390659748)
at 461f6e414d5b2beff547493ec9a719d612cdf8d8 failed in the first_recovery
child before daemon assembly. Its closed reason was
cold_construction/cold_retained_gateway_selection_kind. The parent and child
ran for 401.45 and 401.43 seconds; manifest controls and independent fixture
cleanup passed. Raw log SHA-256:
5556bea295dee1fc3382271a4b3344ce5823dba9d8c9c588ec321a838f9aec31.

The source order explains the mismatch. Startup performs A0 recovery before
constructing the generated domain. Recovery commits the protected terminal,
installs a rebind-origin CurrentV1 route state, advances SQL current authority,
and clears the active fence. Cold construction then selects that current
state, but the retained-gateway helper rejects its form.

The closed diagnostic alone does not prove authority. The repair must bind
the selected state to its complete protected history and exact physical
resources before accepting the retained V1 proof.

## Authority contract

Keep active CurrentV1 authority and retained original-upgrade evidence
distinct. SQL current, the protected current state and its terminal must
agree. The existing gatewayCurrentPredecessorRetirementActionFor validator
must resolve the complete predecessor chain to one original committed
upgrade. Reuse its traversal rather than introducing another interpretation
of protected history.

Require the original V1 source, committed V2 state and journal, upgrade
selection, native lineage and topology to agree. Clone the retained evidence
into the private cold-start expectation. Its historical source must not
replace the current gateway identity or current-state digest.

Missing, partial, crossed, duplicated, ambiguous, cyclic or non-decreasing
history remains a refusal. A bare CurrentV1 kind, resource name, diagnostic,
or partial predecessor record grants no authority. Rebind-origin CurrentV2
remains outside this compatibility repair.

The existing stopped-V1 proof still checks the pinned image, exact owned
container/network/volume, protected volume identity, labels, stopped runtime,
restart configuration, physical census and final reobservation. Fresh
selection and the complete proof must remain consistent through construction.
No current route, SQL history, protected terminal or physical resource is
rewritten by this proof.

## Verification contract and limits

The authentic prepared-A0 recovery test must establish the resulting SQL
current, protected current/terminal and original upgrade projection while
preserving replay, fence and effect assertions. The retained-V1 fixture must
then exercise that projection through the private cold proof, with exact
reads, final reobservation and no mutation or capacity effects.

Negative controls must retain zero proof and refuse before later reads or
effects when required authority is missing, crossed or ambiguous. Existing
NativeV2 and upgrade-origin CurrentV2 proof controls remain required.
Selected tests must be discovered and executed from fresh binaries, with
retained raw output, source/binary hashes and failure history. Formatting,
scoped vet, canonical generation, diff and documentation checks are required
before publication.

These are separate pieces of evidence. The existing public-constructor
fixture refuses earlier at physical identity, so it cannot be represented as
end-to-end acceptance of this repair. A hosted A0 rerun must establish the
actual recovery journey. This change does not reconstruct restart capacity,
authorize incomplete recovery, repair the independent TLS retirement failure,
or complete E6 or M4.

## Recorded local evidence

The reviewed five-file source passed a fresh Windows test run on 2026-10-26,
based on 461f6e414d5b2beff547493ec9a719d612cdf8d8. The retained binary
discovered all six selected parents. Its focused run passed six parents and
nine distinct named subcases in 186.50 seconds, with no failures or skips.
Isolation produced 40 raw RUN/PASS events; these are not 40 distinct tests.

Coverage includes authentic prepared-A0 recovery, the complete retained proof,
missing original source, crossed journal, missing checkpoint, duplicate
history, crossed upgrade-generation metadata, and the three existing format
and read-attribution regression parents. Alias controls mutate populated
nested entries in both directions. Generation and operation metadata
crossings produce no projection. The cycle and non-decreasing traversal
guards are unchanged and source-reviewed; this run does not newly execute
those negative traversal cases.

The fresh compile and exact test discovery passed. The five-file gofmt check,
go vet ./internal/generatedingress, bundled PowerShell
scripts/check-generation.ps1, and git diff --check passed. Source hashes
remained unchanged through verification. Source security review found no
blocking defect in those frozen bytes.

- Source manifest SHA-256:
  66b2a85dbcf2de8aefb5a348a49d33d06568b5b7c2cd933112a7fe088c2256d4.
- Test binary SHA-256:
  56d97802f621262829c59981f2360f551a13e8688f1d0b0befdd033c929f9a94.
- Focused raw output SHA-256:
  c47b3099c1da7c6c7e90e133568ba11871b750d5fbe305b21b9121a203029eb0.

Docker and race checks were not run locally. The real hosted A0 recovery
journey remains the acceptance gate; these local checks do not close it.

## Rollback

Revert the compatibility proof through the PR path, preserving immutable
history and evidence. The earlier implementation will again reject this
recovered CurrentV1 form. No migration, database provisioning or resource
cleanup is involved.

## Hosted recovery result and correspondence diagnostics

[PR #243's first A0 run](https://github.com/tyhuang9/rig/actions/runs/40271403317/job/121404806749)
at 5357344e57969df1effed8d10d797290aaf24da7 passed the earlier retained-selection
boundary, then failed in first_recovery at
cold_construction/cold_group_correspondence. The child ran for 431.47 seconds;
independent fixture cleanup succeeded. Raw log SHA-256:
266699c641c748bcaa18aa7c3e85e9329f569769dcd50dffeaf18684d163c19f.
This is progress through construction, but A0 remains unaccepted.

Five different existing refusal sites shared that reason. The follow-up
assigns each site a fixed reason after its original predicate has failed:

| Failed obligation | Fixed reason |
| --- | --- |
| Verify absence of a non-present component | cold_group_component_absence |
| Match a selected container ID and name | cold_group_container_correspondence |
| Match a selected network name | cold_group_network_correspondence |
| Match a selected image and its content ID | cold_group_image_correspondence |
| Account for the remaining selected object kind | cold_group_other_correspondence |

An absence-verification failure can mean an inspection error or a found
component. The reason does not assert presence. The original predicates,
ordering, inspection calls, refusal sentinel and nested causes remain intact.
The legacy aggregate reason stays in the closed parser for compatibility.
The hostd projections accept only the enumerated reasons in cold_construction;
they do not emit object IDs, names, paths, labels or private error text.

These diagnostics identify the refused obligation. They cannot establish the
responsible object, cross-process physical stability or a safe ownership
exception. A new authentic hosted A0 run is required before choosing a
behavioral repair. The separate restart-accounting defect, live-fixture
composition failures and aggregate race-test timeouts remain open.

### Local diagnostic verification

The final focused evidence covers five ingress parents and one hostd parent.
The initial fresh binary passed the existing positive roster, crossed-container
roster and closed diagnostic grammar. Two new fixture controls failed because
an empty network ID matched an empty builder ID and later refusals already
carried an absent retained-proof envelope. Those failures remain recorded.

The corrected fresh binary passed both affected ingress parents and all four
new cases. A fresh hostd binary passed TestLiveHostdRebindClosedDiagnosticControls,
which exercises both affected hostd projection surfaces and rejection of
crossed stage/reason pairs. The unchanged passing parents are reused with
source bindings; this is not a claim that the initial entire run passed.
No selected case was skipped.

Before retained-proof creation, the control requires an empty proof. Later
refusals require absent presence, a nonempty digest, nil authority and no
retained objects; they do not compare exact digest bytes. Each new control
checks the refusal sentinel and excludes mutating, capacity-probe and
interactive gateway commands after its measurement point.

Verification used the pinned Windows Go 1.26.7 toolchain, GOTOOLCHAIN=local,
GOFLAGS=-mod=readonly -p=1 -buildvcs=false and CGO_ENABLED=0. Exact discovery,
six-file gofmt, scoped ingress and live_docker hostd vet, canonical generation
and git diff --check passed. Source/security review accepted the frozen
successor source. Source manifest SHA-256:
c1b068f88be23bfb1c1ca51bda6a17cec9a1c130e68baf7b8e9cd5daad39f232.

The local controls use simulated Docker responses. They do not replace the
hosted A0 recovery journey or Linux race checks.
