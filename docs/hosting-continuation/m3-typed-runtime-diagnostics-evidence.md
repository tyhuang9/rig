# M3 typed runtime diagnostics evidence

Status: focused native and actual Linux checks, scoped vet, production build
and source reviews completed. Frozen-documentation and final integration
disposition are retained externally. This unit adds private diagnostic
distinctions; it does not establish or repair the unresolved Docker failure
predicate.

## Source and scope

The isolated `feature/hosting-m3-typed-runtime-diagnostics` branch starts at
`0259fed2b8e2a7c6006d0cc3fff52690b7ca417d`. The combined acceptance checkout
remains unchanged. There are no schema, module, workflow, public API or
effect-authority changes in the approved scope. Production factories remain
closed. No publication, merge or deployment is included.

The read-only audit and immutable source/log hashes are retained outside the
checkout at `temp/m3-typed-runtime-diagnostic-20261010.md` and its sibling
`.audit.json`. Eleven relevant production blobs are identical between tested
`788c508111f62830c3fa27b40feb4e5b0a33036b` and the combined base.

## Observed Docker boundaries

- Commit job `114108728935` retained P9 `stage_start_intent` and a running stage.
  It did not establish the complete P10 serving proof or accepted append.
- Rollback job `114108728867` retained P12 `rollback_intent`. Because rollback
  intent uses the previous sequence plus one, the preceding forward prefix was
  P11 `final_config_intent`; P10 serving proof had succeeded. The original
  forward refusal was in final config copy/proof, binding construction or
  append, before a subsequent rollback failure. The forced P17 refusal was not
  reached.
- The latest command class is overwritten by subsequent commands and cannot
  identify the first refusal. The 30-minute journey context does not support
  treating the roughly 72/79-second failures as journey deadline expiry.

No rejected physical predicate is proven. The source audit does not justify
weakening ownership, topology, config, authority, history or fresh-read checks.

A separate actual PR141 stopped-stage replay failure had already passed the
replay operation and protected-history checks, then failed a four-clause
physical assertion. The shared parsers do not create unsorted slices from maps;
the saved output does not establish which clause failed. The narrowly approved
scope addendum splits that assertion in
`gateway_rebind_stage_container_live_test.go` into the same ordered checks with
fixed messages for inspection, snapshot equality, exact binding and owned-name
census. No new reads, normalization, changed predicates or raw values are added.
The actual hosted stopped-stage parent remains its external acceptance gate.

## Diagnostic invariants

Only existing `stable`, `serveStage` and `copyFinalConfig` failure boundaries
receive finite private categories. Existing predicate, read, guard and effect
order/count must remain unchanged. Public error text, diagnostic code and
cancellation handling must remain unchanged. An unwrap chain may contain only
a freshly sanitized boundary error, never a raw upstream error or payload.

The live test decorator delegates once, preserves the returned values/error,
and retains the first failed method even if its checkpoint is unknown. A
successful method followed by coordinator refusal is a separate unresolved
outer binding/append/guard category. It must not be reported as a method
failure. Command observations use fixed classes and bounded result categories;
no command arguments, outputs, configuration, addresses or resource identities
are logged. No new probes or authority callbacks are introduced.

The scoped security source review found preserved short-circuit, read, guard
and effect order/count and unchanged public error/cancellation behavior. Its
record is `security-production-review.json` in the unit artifact directory.
Nested `stable` diagnostics identify the failed predicate, but do not identify
which repeated `stable` invocation within a method failed. Method/progress
evidence narrows chronology without eliminating that ambiguity. This unit
does not add instrumentation merely to remove that remaining precision limit.

## Baseline

The clean base ran this exact selection once with local Go 1.27.0 on
Windows/amd64, `GOTOOLCHAIN=local`, and
`GOFLAGS=-mod=readonly -buildvcs=false -p=1`:

```text
go test ./internal/generatedingress -run ^(TestGatewayRebindTypedStageRuntimeCommandsStartReplayAndCopyFinalConfig|TestGatewayRebindTypedStageRuntimeRejectsAlteredRestartAndLateBoundaryDrift|TestGatewayRebindTypedStageRuntimeRejectsAuthorityDriftAfterFinalArchive|TestGatewayRebindTypedDriverRechecksAuthorityBeforeStageServingRecord|TestLiveGatewayRebindRuntimeDiagnosticRunnerDelegatesAndPreservesError|TestLiveGatewayRebindRuntimeValidatedProgressUpdatesOnlyAfterAppend)$ -count=1 -json -timeout=20m
```

Result: six parents and five leaf subcases passed; no failures or skips.
Package elapsed 139.986 seconds; wrapper wall time 189.937 seconds; actual exit
0. Retained session 83958 was collected. Output SHA256:
`5b0ffcd8e5af0670ebd7c0a47ba3f88682c195b61a7ff69c60c65aa1a9bd9ac4`.

Artifacts are under `temp/m3-typed-runtime-diagnostics-20261010/`:

- `baseline-six-parents.command.json`, `.jsonl` and `.result.json` preserve the
  original command, five initial file hashes and unchanged terminal hashes.
- The additive 791-input manifest was collected at 18:48:03 UTC during
  execution, not before launch. Its original comparison incorrectly reported
  mismatches because of its parser; that record is retained.
- `baseline-input-completeness-correction.json` was collected at 18:49:32 UTC,
  after the package completed at 18:49:04 UTC and before the retained session
  was collected. All 791 tracked Go/module working hashes match the exact
  clean base. This correction is metadata evidence, not a repeated test run.

## Executable diagnostic regression

Before production edits, the new
`TestGatewayRebindTypedStageRuntimeDiagnosticRedBeforeStartGuard` exercised the
real typed stage fixture with a rejecting authority guard. The public boundary
error remained correct, but the expected private checkpoint reporter was
absent. This is the intended diagnostic RED; it does not reproduce the unknown
Docker predicate.

The final parent is named
`TestGatewayRebindTypedStageRuntimeDiagnosticBeforeStartGuard`; the historical
RED receipt retains the earlier `DiagnosticRedBeforeStartGuard` name.

The exact one-parent run exited 1, with one failed parent and no skips, package
elapsed 15.679 seconds. Tracked and new Go/module hashes were unchanged during
the run. The original `red-before-start-guard.command.json`, `.jsonl` and
`.result.json` remain under the same artifact directory. Output SHA256:
`a9a47df3f8aadf5e4a7428a7e7765c45f4e90415ffc7c4b803aa23aa886f30ea`.

## Preserved implementation attempt

The first native diagnostic behavior attempt remains a failed run. The saved
`native-new-diagnostics.command.json`, `.jsonl` and `.result.json` record exit
1, package 107.535 seconds, wall 114.562 seconds, and unchanged source inputs.
Output SHA256:
`f106bd7ebe97aca2ba02b66cb4e183478d896b0b56e87e9fd3f8b829c163402e`.

Five parents passed and the refusal-checkpoint parent failed. Four of its six
cases passed. The altered-endpoint case had supplied another valid identity,
which the existing validator correctly accepted; the test now supplies an
invalid endpoint. The cancelled-read case preserved `DiagnosticCancelled`
but reached `stable/first_read`, rather than the test's expected later guard.
Only those test inputs/expectations changed. No validator was altered. Its
metadata also used underscores for two source-defined hyphenated case names;
the final expected inventory retains the actual names.

The initial recorder source review found that the new snapshots were not yet
consumed at the live failure sites. Both sites now use the same bounded logging
helper. The preserved initial review and final source GO are recorded as
`security-recorder-initial-review.json` and
`security-recorder-final-review.json`. Locked snapshots do not hold their mutex
over a delegated production call, callback or effect. The earliest failed
delegate remains latched even when its category is unknown. A latest successful
Docker command is not presented as a failed command when a later semantic proof
refuses.

## Exact focused inventory

Seven new parents are ordinary unit tests, including those named `TestLive`:

| Parent | Behavior |
| --- | --- |
| `TestGatewayRebindTypedStageRuntimeDiagnosticBeforeStartGuard` | Real stage adapter rejects before reads/effects; public error and private checkpoint |
| `TestGatewayRebindTypedStageRuntimeDiagnosticRefusalCheckpoints` | Six running/config/archive/guard/cancellation cases with effect-count checks |
| `TestGatewayRebindTypedStageDiagnosticSanitizesUnknownError` | Raw upstream error excluded; invalid private enums become unknown |
| `TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverRetainsFirstFailure` | First unknown failure cannot be replaced by later recognized failure |
| `TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverSeparatesSuccessfulMethodFromOuterRefusal` | Successful delegate plus outer refusal remains a separate category |
| `TestLiveGatewayRebindRuntimeDiagnosticRunnerCapturesBoundedResult` | Four success/failure/cancelled/deadline result cases; original result/error retained |
| `TestLiveGatewayRebindRuntimeFailureDiagnosticUsesBoundedSnapshots` | Actual failure-diagnostic assembly consumes fixed snapshots and validated progress |

The six retained adjacent parents are the exact baseline selection above. The
full actual Linux selection therefore requires 13 unique parents and 15 leaf
subcases; the representative Windows selection requires six new parents and
four leaves, excluding the six-case refusal matrix exercised on Linux. The
command receipts contain the complete exact leaf inventory, including
`post-start_guard_refusal` and `post-copy_guard_refusal`.

`ci-selection.json` maps all seven new parents and the five existing parents in
modified test files to exactly one of the existing 18 required ingress
partitions. The three workflow matrices agree and their blobs are unchanged.
This is static selector evidence, not Bash, jq, race or hosted execution.

## Final verification

Final commands use local Go 1.27.0 with read-only modules,
`GOTOOLCHAIN=local`, `-p=1`, and `CGO_ENABLED=0`. Each command receipt pins all
tracked and new Go files plus `go.mod` and `go.sum` before and after execution.
Live opt-ins are removed. Exact discovery, every selected parent/leaf run and
pass exactly once, terminal exit, package PASS and absence of failures, skips
and panic are required.

| Gate | Result | Immutable receipt |
| --- | --- | --- |
| Representative Windows diagnostics | Six parents/four leaves PASS; discovery/test exit 0; package 15.242s, wall 25.250s | `final-native-fast-diagnostics.result.json` |
| Actual WSL diagnostics plus adjacent behavior | 13 parents/15 leaves PASS; compile/discovery/Linux exits 0; total wrapper 71.046s | `final-wsl-full-diagnostics.result.json` |
| Scoped vet and four production CLI builds | Both exit 0; source unchanged | `final-static-diagnostics.result.json` |
| Diff check | Exit 0; ordinary Git line-ending notices only | `final-static-diagnostics.diff-check.log` |
| Canonical formatting | Five files match `gofmt`; no source writes | `canonical-format-assessment.json` |

The native result's raw JSON SHA256 is
`7b2bcce30d6bc101323d0c4b5eb3d45e7489e897e18f6bf46242ddf1df994b87`.
Retained session 84110 was collected with wrapper exit 0.

The actual Linux binary output SHA256 is
`bab43c83f99af7845473079f41d1e1d17c82e9ffd540460363839bb1ff19f642`.
Session 40862 was collected with wrapper exit 0. Windows Go cross-compiled the
Linux/amd64 binary, which was then discovered and executed in WSL from the
actual package directory. Both final behavior gates and the static/build gate
pin 793 Go/module inputs, all unchanged. These are plain tests with simulated
Docker adapters; they are not Linux race tests or actual Docker execution.

The original static wrapper remains `passed: false`: its `gofmt -d` step
reported the stopped-stage file's 687 CRLF lines as a full-file LF difference.
Vet, production build and diff check each succeeded; session 40267 returned 1
for that formatting result. The separate canonical assessment normalizes CRLF
only in memory and compares `gofmt` stdin output, proving equality for all five
files without changing a working byte. It does not replace or claim success of
the original wrapper. No behavior check was repeated for this metadata issue.

The production command was:

```text
go vet ./internal/generatedingress
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
git diff --check
```

The evidence artifact directory also retains exact native/WSL command argv,
the complete discovered/tested inventory, log hashes, and the five reviewed
source hashes in `launch-source-freeze.json`. Source review by the security and
final integration specialists found no blocker; final integration disposition
depends on actual remaining execution and the frozen documentation checks.

## Remaining acceptance

Final integration review and documentation outcomes are recorded after this
page is frozen. Root owns those documentation verification receipts. Its frozen,
offline documentation install passed with unchanged manifests/lockfile;
`temp/m3-typed-runtime-diagnostics-docs-install-normal-user-20261010/result.json`
retains the outcome. The final documentation commands are
`pnpm --dir docs check:workflow`, `pnpm --dir docs build`, and
`pnpm --dir docs check:accessibility`; the external receipt at
`temp/m3-typed-runtime-diagnostics-docs-check-normal-user-20261010/result.json`
establishes their outcomes without rewriting this checked page merely to insert
its own hash.

The local planned diagnostic cases are covered by the selected tests and source
reviews. The conservative outer-refusal case proves the recording distinction;
it does not independently inject every possible coordinator binding/append
failure. Repeated `stable` invocations remain intentionally indistinguishable
within one method. No full-package rerun, local race run or physical LAN claim
is made. The stopped-stage assertion split was compiled and reviewed but its
live Docker parent was not executed locally.

Local Docker is unavailable. Both hosted opt-in typed runtime parents remain
required: `TestLiveGatewayRebindRuntimeCommitAndReplay` and
`TestLiveGatewayRebindRuntimeRollbackAndReplay`. Neither local simulated
adapter tests nor successful private handover tests establish those journeys,
full-controller crash coverage or physical second-device LAN acceptance.
