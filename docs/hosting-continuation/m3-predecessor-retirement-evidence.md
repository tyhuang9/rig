# M3 terminal predecessor retirement — acceptance evidence

Status: targeted Linux and Windows runtime acceptance passed. Hosted Docker,
race-detector, physical NIC and second-device LAN acceptance remain open.

## Requirement and scope

The durable M3 rebind contract requires startup and recovery replay to recheck
predecessor retirement at successor-ready, database-committed and committed
states. A returning old NIC must not allow an old gateway container to serve
again. Exact journal-bound withdrawal and negative old-listener proof must
precede normal workers and successor publication. Terminal SQL/history remains
immutable. See `m3-rebind-cutover-contract.md`, predecessor shutdown section and
acceptance gate 7.

This cohesive branch starts at published
`f49cf793bfae06f3d28fa45d4609a9489075cccb`, with no integration of held stage
projection or independent fixture/lifecycle branches. Its purpose is to retire
every exact committed predecessor before terminal startup resumes serving.

## Reviewed design invariants

- Derive the entire committed predecessor chain through the native root before
  the first predecessor effect. Reject gaps, cycles, duplicate identities and
  crossed ownership. Exclude selected current, aborted and unrelated resources
  from retirement. Independently authorized exact-current quarantine on refusal
  remains distinct and must not be counted as predecessor cleanup.
- Acquire the existing deployment-effects lease and gateway locks. Revalidate
  exact SQL/protected authority before and after effects.
- Withdraw only immutable owned predecessor IDs. Do not create, start, remove,
  reconfigure or rewrite historical resources as part of retirement.
- Prove stopped, not restarting and zero effective binds through two stable
  inventories per ancestor. Stop/prove the full chain before negative listener
  checks, because another exact ancestor may still own a shared old tuple while
  the chain is being retired. A missing old address never establishes
  serving/404 behavior.
- Require negative proof for returned predecessor-exclusive listeners. A present
  listener on a shared successor tuple requires exact-current attribution and
  fresh attestation. If selected current is stopped, require listener absence;
  do not demand a serving proof from a stopped current or silently omit the tuple.
- Cover common startup dispatch, including pending route/LAN recovery, as well
  as direct restore/recovery callers. Later physical-effect guards freshly
  observe retirement to reject ancestor revival after lock reacquisition.
- Withdrawal authority does not depend on serving roles or obsolete app
  endpoints. Uncertainty keeps normal preparation closed, invokes existing
  exact-selected-current quarantine only when safely provable, and retains the
  predecessor may-live diagnostic. It never authorizes unrelated stops.
- A correctly owned restarting predecessor is eligible for exact withdrawal;
  immutable ownership checks precede the stop, and strict stopped/not-restarting
  state is required afterward. Ordinary serving comparators keep their existing
  semantics; runtime flags must not be normalized to manufacture ownership proof.
- Preserve immutable SQL/protected history, dormant production rebind factories,
  application-owned external databases and scoped runtime-secret boundaries.

## Existing baseline

The worker launched one retained native Go invocation on the clean f49 worktree:

```text
go test -json ./internal/generatedingress ./cmd/hostd -run '^(TestGatewayRebindConcreteCompositionCommitsAndReplaysSameDockerState|TestGatewayRebindStoppedCurrentPredecessorDoesNotClaimListenerAbsence|TestGatewayRebindRollbackPredecessorRequiresUnoccupiedListeners|TestGatewayRebindStartupDispatchPreservesDedicatedRecovery|TestGatewayRebindStartupActiveResultAndLeaseOrdering|TestGatewayRebindStartupRefusesUnverifiedRecovery)$' -count=1
```

`GOFLAGS=-mod=readonly -buildvcs=false`; native Go 1.27.0, Windows/amd64,
CGO disabled, local toolchain. The original launcher did not capture every
environment/source field, so additive byte and toolchain sidecars supplement
the immutable original command/result. No baseline was rerun for metadata.

The retained session39910 completed and was reaped: exit 0, wall 213.727 seconds,
all six expected parent tests passed and none failed or skipped. The ingress
package took 192.066 seconds. This includes retained terminal commit/replay and
stopped-successor restore, but it does not exercise a revived predecessor.

Original receipts remain in `temp/m3-predecessor-retirement-20261009/`:

| Artifact | SHA-256 |
| --- | --- |
| `baseline-go-test.json` | `C151FB85D61FBD06BC6CFC9F124B88C2F43D3E5F4DE54EA3F1993BF34E7B937A` |
| `baseline-result.json` | `190D06236D9A2B67ACA998F2B6F82AA916952C3FEFDA9CCED45FEAB3CD750BAE` |
| `baseline-source-manifest.sha256` | `34F1B18C7204CD30B04CEDA621768DD99D4C84C40E70C5F815D062849393A101` |

### Regression construction

The first new regression attempt failed compilation because its fixture read a
nonexistent `NetworkObservation` field. Exit 1, wall 4.107 seconds; production
was unchanged. `red-command.json`, `red-go-test.json`, `red-result.json` and
`red-source-manifest.sha256` retain that attempt. It is a test-construction
error and establishes no runtime regression.

The next attempt (`red2-*`) compiled, ran the parent and failed with
`route_reconciliation_required`, no Docker effects, exit 1, wall 113.284 seconds.
Review found that the test called terminal recovery with a stopped selected
current, omitting hostd's preceding serving-restore call. This is not accepted
as proof of the predecessor-retirement defect. Its original source and output
remain preserved. The corrected regression follows the actual Restore then
Recover sequence, retains the network observer across fresh Manager instances,
and checks old listener absence, exact effect order and immutable history.
### Meaningful failing regression

The reviewed corrected attempt (`red3-*`) reached the exact returned-native
predecessor/stopped-current boundary through the real serving-restore call.
Fixture preconditions proved the unique pinned returned address and an occupied
old listener. Restore returned `route_reconciliation_required`, dispatched zero
effects, and left the exact old predecessor running. Exit 1, wall 111.210 seconds,
package 104.402 seconds. This demonstrates missing retirement and recovery; it
does not demonstrate that the old startup path admitted normal workers.

The test source SHA-256 was
`BB73FB53709E6008403465B5DCC0C25E02FE5C8828711005B43651412383C0E8`.
All 298 pre-existing package Go source pins match the clean baseline; the only
additional file is the regression. The original RED manifest omitted module
files. An additive post-run check matched both module hashes to baseline; the
command used read-only module flags. That supplement is not described as a
contemporaneous module capture. The original failed completeness assessment and
the supplement remain separate (`root-red3-source-audit*.json`).

| Artifact | SHA-256 |
| --- | --- |
| `red3-source-before.sha256` | `124185BE2746A66449F72A69C14D12DCA6DA74F6364D7F4675510F7F712D8BFC` |
| `red3-go-test.json` | `785604EA8868F6163C62ADA28C62DB2F7F64905BAD5343BAA8E75622BA037567` |

Production implementation began only after this failing result. Passing
corrected behavior and the broader reviewed failure matrix remain required.

### Documentation dependency preparation

The new worktree's documentation dependencies were installed using the declared
pnpm entrypoint with `--dir docs install --frozen-lockfile --offline`: exit 0,
2.762 seconds, every manifest/lock hash unchanged. The original output and
result are `root-docs-install-output.txt` and `root-docs-install-result.json` in
the temporary evidence directory. Output SHA-256:
`169BD2B93517CDBD79FA942EE3B7FF7D6DEFBB5E533CA10E50D051B9AACCB523`.
This is dependency preparation only, not documentation-build acceptance.

## Acceptance boundary

The source-pinned failing regression is recorded above. The corrected Linux and
Windows runtime results below cover exact stop-before-start ordering,
ownership/authority drift, lost acknowledgment, absent and returned addresses,
shared listeners, pending recovery dispatch, history immutability and replay.
The earlier full-chain derivation and canonical legacy-history parents passed
in the first Linux matrix; their evidence remains tied to that run's source
pins. They were not rerun after the native volume-census correction. Caller,
build, static and independent source-review results are recorded separately.

Simulated topology and plain WSL must not be reported as live Docker, race,
physical NIC or second-device LAN acceptance. Those broader gates remain open.

## Native hostd startup caller verification

The hostd dispatcher now requires predecessor retirement and a fresh, equal
full inspection before any rebound startup path can continue. This includes
stable, pending route, pending LAN, incomplete batch and completed batch modes.
Active recovery first verifies its terminal result and fresh inspection, then
uses the same boundary. An exact ABORT to native keeps the original native
startup path. Normal startup admission acquires its lease only after the
recovery callbacks release theirs.

The frozen two-file caller change passed independent caller/source and security
review, conditional on the generated-ingress retirement implementation. Native
Windows Go 1.27.0 execution then passed all seven selected parents and 83
subcases, no failures/skips, exit 0, wall 3.484 seconds:

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -json -timeout=10m -run '^(TestGatewayRebindStartupActiveResultAndLeaseOrdering|TestGatewayRebindStartupDispatchPreservesDedicatedRecovery|TestGatewayRebindStartupFreshDatabaseAndManagerReuse|TestGatewayRebindStartupRefusesUnverifiedRecovery|TestGatewayRebindStartupRetirementPinsEveryReboundMode|TestGatewayRebindStartupRetirementRequiresExactCurrentKind|TestGatewayRebindStartupVerifiesActiveResultBeforeRetirement)$' ./cmd/hostd
```

All production Go files, hostd test files and module inputs remained unchanged
before/after execution. Unrelated package test files are excluded from this
pin set because this command does not compile them; they remained independently
owned by the runtime-test worker. Exact expected parent discovery and subcase
counts were checked, including 35 refusal cases across five rebound modes.
The tests exercise actual lease acquisition/release and a real fresh database,
with callback fixtures for recovery ordering. They do not prove physical
predecessor withdrawal.

Artifacts: `temp/m3-predecessor-retirement-20261009/root-hostd-first-native/`.
Output SHA-256:
`0C73B3F1CF5748CE25CAB4F6BDCCC13A4D391B2B4977B0455B18D18DAAFD5BE5`.
Caller source/test SHA-256 values at execution:
`4E4EF6BD1A965F0D02F48C231D30E1031D288B5E79B858E95A4056C3F0121702`
and `E251A30187C9260B4FA234905B79972CB5EC55665A3EEDCFC0ED975936C30461`.
The Windows receipt above predates the final generated-ingress error-path
corrections. It establishes caller behavior at its recorded source pins; the
later Linux and CLI checks below used the corrected production source.

## Linux caller and CLI checks before the runtime census correction

After the four final runtime source corrections passed independent security
review, the caller test binary was cross-compiled with Go 1.27.0 for Linux/amd64,
CGO disabled, then actually executed through WSL from `cmd/hostd`. The build used
`go test -mod=readonly -buildvcs=false -p=1 -c -o <retained-binary> ./cmd/hostd`.
Execution used `-test.v -test.count=1 -test.timeout=10m` and the same seven exact
parent names shown above. The Linux process had an empty environment except
the declared executable search path, preventing inherited live-test opt-ins.

Build and execution both exited 0: seven parents and 83 subcases passed, none
failed or skipped, wall 18.734 seconds. All production Go, hostd tests and
module hashes remained unchanged during the check. The full command, source
pins, binary digest and raw output are retained under
`temp/m3-predecessor-retirement-20261009/root-hostd-final-wsl/`. Run-output
SHA-256: `34A6E5166A9882E36999A4760279E09181426723FB8281911F7AD85A83389D49`.
This is actual Linux caller verification, not Docker or race-detector evidence.

The canonical four-CLI build then passed on native Windows/amd64:

```text
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

It used Go 1.27.0, CGO disabled and read-only module/build-VCS flags; exit 0,
wall 4.578 seconds, empty build output, all production and module hashes
unchanged. Receipt: `root-four-cli-final-native/` in the same temporary evidence
directory. Generated-ingress test files were excluded from these caller/build
pin sets because neither command compiled them; their independent QA work was
still in progress. The complete runtime matrix remains open.

## First complete Linux runtime gate

The first frozen runtime matrix was compiled for Linux/amd64 and actually run
through WSL, selecting eight new or strengthened parents and three retained
contracts. Build exited 0; Linux execution exited 1 after 154.969 seconds.
All 11 expected parents were discovered: seven passed and four failed. Of 22
discovered subcases, 15 passed. Nothing skipped. Generated-ingress Go sources
and module hashes remained unchanged during this run.

Passing parents covered raw rebind ownership/withdrawal, uncertainty precedence,
full ancestor-chain derivation, canonical legacy lineage, the direct startup
retirement entrypoint, complete SQL serving authority and lost-authority refusal.
These partial passes do not establish runtime acceptance.

Failures were:

- Retained exact-marker reconciliation, refusing before its injected runtime
  observed or reconciled anything.
- The new pending-marker shared-listener positive.
- Serving-restore ordering and the intended early-history-failure case.
- The returned-native journey, including native inventory rows and the final
  revival check, which did not reach the intended listener/sweep boundary.

The immutable run is retained under the `final-wsl-retirement-matrix` prefix in
the temporary evidence directory. Run-log SHA-256:
`110504C196D910F9CD3DCCA48AB531E735D24B09F9DC97E568D2D7F3E11C489C`.
The original wrapper also compared parent order, but this was a genuine failed
test execution; a verifier correction alone cannot make it pass. Diagnosis must
separate fixture setup defects from production regressions before targeted
reverification. Final native runtime, static and delivery checks remain open.

### Native volume census correction

Diagnosis found a production ordering defect in the new native predecessor
reader: aggregate volume validation ran before the real owned-volume census
had populated the observation. The reader now assigns the existing census
result before invoking the unchanged aggregate validator. Exact ownership,
resource names and volume-consumer checks remain required. Independent security
review accepted this correction; the corrected runtime matrix remains open.

The corrected production source passed a fresh actual WSL execution of the
seven hostd parents and 83 subcases above: build and execution exited 0, no
failures or skips, wall 16.312 seconds, all captured source/module pins
unchanged. Artifacts: `root-hostd-census-corrected-wsl/` in the temporary
evidence directory. Run-output SHA-256:
`898361567FA0A7BB62A6E441A5CE8ECB04AD05E220EF44E5DC40B5529DC20954`.

The canonical four-CLI native build also passed on the corrected source:
exit 0, wall 5.937 seconds, empty output and all captured production/module
pins unchanged. Receipt: `root-four-cli-census-corrected-native/`. These checks
establish startup caller behavior and compilation; the native predecessor
reader itself still requires the corrected runtime tests.

### Focused corrected Linux run

The next run selected the four previously failing parents and retained
lost-acknowledgment reconciliation. Build exited 0; execution exited 1, wall
44.203 seconds, all captured generated-ingress and module pins unchanged.
Exact-marker and lost-acknowledgment reconciliation passed. All ten native
state/address subcases passed, and the actual composed journey reached exact
predecessor stop followed by current start. Its parent still failed because
the test compared Docker's raw inspection ID with the canonical retained
journal ID used by the command.

Serving ordering and role-revocation subcases passed. The intended history
failure fixture incorrectly assumed legacy history instead of the typed
history it had created. The pending-marker listener positive continued to
refuse current serving and remained under diagnosis. These are partial results;
the five-parent gate did not pass and the later composition assertions were
not reached.

Artifacts use the `corrected-focus-wsl-marker-restore-native` prefix in the
temporary evidence directory. Run-log SHA-256:
`B99E1EF36211704A80DC46565F55A1171C072563BF4406B5C2F96B5A2B1F9DC1`.

A separate one-parent diagnostic localized the pending-marker failure:
observation succeeded with `recovery_effective` and matched its target, but
correctly failed selected-marker matching. The fixture had used the future
persisted revision instead of the selected marker's sanctioned projection.
The test now uses `gatewayCurrentPhysicalTargetsForSelection`; both existing
attestation predicates remain required. Temporary diagnostic instrumentation
was removed. That diagnostic run exited 1 after 18.172 seconds, with unchanged
source pins; it is not acceptance of the corrected test. Its run-log SHA-256 is
`9C7EFCD73CBEEF39DFB86AA91664759A3AA676908D1EB6E3DA29A42DA3B156FB`
under the `shared-marker-pending-proof-diagnostic` artifact prefix.

## Corrected Linux runtime acceptance

The frozen final run compiled the generated-ingress test binary, discovered its
selected tests from the binary, and executed it through WSL. Build, discovery
and Linux execution all exited 0; wall 122.297 seconds. All 17 expected parents
and 29 leaf subcases passed exactly once, with no failures, skips or panics.
All captured Go and module hashes were unchanged.

The selected groups cover the previously failing marker reconciliation,
pending-marker shared listener, post-selection history failure and returned
native journey, plus affected batch, concrete serving-restore and LAN authority
contracts. The returned-native parent reached its later immutable-history,
effect-free replay and final-inventory revival assertions. Compatibility unit
fixtures pin predecessor actions derived from real SQL/protected history;
their bounded observations are not raw Docker ownership acceptance. The
returned-native composition exercises the actual native inventory reader with
a simulated Docker executor.

The original verifier mistakenly expected nine raw-reader subcases belonging
to a parent outside its 17-parent selection. Consequently its wrapper exited 1
and its original result remains `passed=false`, despite the successful Linux
process. Both are preserved. Separate QA and root assessments read the same
immutable log, require exact selected parent and leaf-case sets with one pass
each, verify discovery, process exits and source hashes, and exclude only those
nine unselected cases. No test was rerun to correct metadata. The separate
native gate below executed those nine cases.

Artifacts use the `final17-wsl-retirement-matrix` prefix, including
`.corrected-assessment.json`; the independent root assessment is
`root-independent-final17-assessment.json` in the temporary evidence directory.
Run-log SHA-256:
`98323F1CC346BC51FC234654E2DF656013E025BF569C520C508B130BCA5575DC`.
Preserved original result SHA-256:
`86802E63E3AB88DA5AF081F871E3E57DFF279C2E3C39D4D0E17F1BE19D7D3835`.

## Native Windows runtime acceptance

The frozen source passed the two selected native parents:

```text
go test -json -count=1 -timeout=5m -run '^(TestGatewayCurrentPredecessorRetirementFailurePreservesStrongestUncertainty|TestManagerGatewayCurrentPhysicalRuntimeStopsExactRetirementTarget)$' ./internal/generatedingress
```

Go test discovery and execution exited 0. Both parents and all nine raw-reader
subcases passed exactly once, with no failures, skips or panics. Package time
was 25.198 seconds; total wall time was 45.813 seconds. Source and module hashes
were unchanged. The raw-reader rows cover running, restarting, paused, dead,
effective-port-only, already-stopped, absent, crossed-owner and lost-acknowledgment
states. This is simulated Docker ownership/withdrawal on Windows, not live
Docker or race-detector acceptance.

The original wrapper incorrectly matched uppercase Go JSON event names and
therefore recorded `passed=false`. Its result remains preserved. Separate QA
and root assessments parse the actual lowercase `run` and `pass` events,
require the exact two parents and nine subcases once each, package success,
discovery/execution exit 0 and unchanged source pins. No test was rerun for
this parser correction.

Artifacts use the `final17-native-uncertainty-raw-reader` prefix, with the
independent root assessment in `root-independent-native2-assessment.json`.
Raw JSON SHA-256:
`FBA8E8D28D7FECAC922937CB34DE2D041D936C40974309897E5E0335A0B4A08C`.
Preserved original result SHA-256:
`7B9E12FE7AE4808BE95E36776D0681DDC3DAE3A1BB514721DE5F40B569C95C9C`.

## Scoped static verification

On the frozen corrected source, these native checks passed:

- `go vet ./internal/generatedingress ./cmd/hostd`: exit 0, 3.547 seconds,
  empty output.
- `gofmt -l` on all 17 changed Go files: exit 0, empty output.
- `git diff --check`: exit 0; ordinary Windows line-ending notices only.

All Go and module hashes were unchanged before and after the checks. The
toolchain was Go 1.27.0, Windows/amd64, CGO disabled, with read-only module and
build-VCS flags. Commands, complete source pins and logs are retained under
`root-static-final-native/` in the temporary evidence directory.

## CI selection audit

The final static selection audit covers all 11 new or strengthened parent names,
including the three compatibility refusal controls. Each name maps to exactly
one of the 18 required generated-ingress partitions, and that partition appears
in both the Linux race and Windows plain-test workflow matrices. No CI changes
were necessary. The audit parses the actual runner filters and both matrices;
it does not establish compilation, discovery or execution.

Receipt: `root-ci-selection-complete.json` in the temporary evidence directory.
The original eight-name receipt is preserved separately. CI source SHA-256:

- `scripts/check-generated-runtime-race.sh`:
  `3C7BBA0567D3C77C2518BD479C00804B04B2D24F86C31FC808C8B585273D02AC`
- `.github/workflows/github-deployment-ci.yml`:
  `FB049E5CE09F5375C1F7138519176592A9188EEDB20EF2D62768B8008707275F`
- `.github/workflows/windows-controller-ci.yml`:
  `71EA8DD05F6D607EEE2D583AFF769F0578AC9186C1647CB6B6ED3194EF25CDBE`

## Review status

Independent source security review accepted the final production corrections,
including exact selected-marker attribution, failure cleanup and preservation
of the strongest may-live diagnostic. The orchestrator and root also inspected
the production diff. These source reviews do not establish runtime acceptance.

The CodeRabbit CLI review produced no assessment: the restricted WSL attempt
was denied, and the permitted normal-user prerequisite check confirmed that
`coderabbit` is not installed. No installation, authentication or remote scan
was attempted. Aggregate final-integration source review found no remaining
blocker; its delivery decision requires the final documentation check result.
The publication proposal records that decision and the observed documentation
outcomes after this evidence source is frozen.

## Final documentation gate

Run the repository's declared documentation commands against the finalized
evidence source:

```text
pnpm --dir docs check:workflow
pnpm --dir docs build
pnpm --dir docs check:accessibility
```

The check wrapper records each command, exit status, output hash and before/after
documentation input hashes under
`temp/m3-predecessor-retirement-docs-check-normal-user-20261009/`. The publication
proposal records the actual outcomes. This command list alone is not a passing
result.

## Inherited hosted limitation

This branch starts at published f49 and does not contain the separately reviewed
stage-projection repair at `788c508`. The fixture-only PR138, whose ingress
source is unchanged from f49, completed its
[hosted gateway run](https://github.com/tyhuang9/rig/actions/runs/37997312056)
with 11 passing jobs and four failures. Both final-handover cases reproduced
the earlier `final_config_copied` / `stage_runtime` refusal. Commit and rollback
replay also failed with `route_reconciliation_required`, retaining resources
when cleanup could not acquire the gateway lock. Their diagnostic sequences
varied from the earlier run; an identical trace is not claimed.

That run is evidence about the published base behavior, not acceptance of this
unpublished retirement change. Hosted execution of the separate projection
repair remains necessary to establish which physical failures it resolves.

## Recovery and rollback boundary

Retain terminal SQL/protected state during every refusal. Do not delete history,
move profile heads backward or bypass recovery to force serving. Reverting this
local branch would restore the predecessor-startup gap; it is not an operational
workaround for an ambiguous running predecessor. Publication, merge, deployment
and controller activation require their applicable explicit authorization.
