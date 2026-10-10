# M3 typed stage-copy refusal diagnostics

Status: local source and scoped executable verification complete; documentation
checks and final integration review use the frozen page and external receipts.
This page is not a Docker acceptance record.

## Source and observed boundary

This unit starts at `096b3c7b0993d67f66f73ce2aa4baef83875125d` on
`feature/hosting-m3-stage-copy-diagnostics`. The published combined and diagnostic
checkouts remain unchanged. No commits from other branches are integrated here.

PR143's typed Docker commit job `114317792862` ran the combined `0259fed` tree and
failed after retained sequence 7 (`stage_config_intent`). The fixed latest-command
category was unknown. The final failure and cleanup failure did not establish
which copy, observation, guard, binding or progress append first refused. The
immutable job response is retained in the wrapper workspace at
`temp/pr143-combined-hosted-acceptance-20261010/gateway-typed-commit-failure/job-114317792862.json`.

The original diagnostic driver only overrode `serveStage` and `copyFinalConfig`.
Its promoted `copyStageConfig` method bypassed first-failure recording, and that
method collapsed nested stable-observation errors to the public generic refusal.
This source gap is established; the cause of the hosted physical refusal remains
unproven. The separate typed rollback failure at retained sequence 12 must not be
treated as the same proven cause.

PR144 job `114317822835`, at the exact `096b3c7` tree, subsequently failed with
`refused=false` and a nil public error (100.50 seconds); required cleanup passed.
This is a different observation from PR143's retained-P12 error and failed
cleanup. The failed-expectation branch called the existing logger only when the
public error was non-nil, so it emitted no retained stage snapshot. A nil return
does not establish a committed disposition; an earlier safe abort remains
possible. This unit also removes only that inner test-only logging guard, keeping
the failed expectation and injection unchanged, and checks extraction with a nil
outer error and an earlier retained delegation failure. The raw PR144 log SHA256
is `f39b162b48a55fb2ee623df511d1b7d58d3286ef686c43fa9e107f9918d57df0` in
`temp/pr144-diagnostics-hosted-acceptance-20261010/job-114317822835.log`.

PR144 typed commit job `114317822965` also failed at a different retained boundary:
sequence 6, `rollback_intent`, with unknown first stage method/operation/checkpoint
and a latest successful `network_ls` command. The parent took 55.40 seconds and
required cleanup failed after a lock refusal. The successful latest command is
not evidence that it caused the earlier refusal. Raw SHA256:
`5809843b00458b81f00622d8951f7881fdaeb6c8d66f5312a5e17d6959869bbc`, in the same
PR144 artifact folder. The explicit eight-method decorator removes the promoted
method recording gap; no additional production checkpoint or semantic repair is
inferred from this observation. The stopped-stage component passed on PR144,
which does not explain its earlier PR143 failure.

## Bounded change and invariants

- Add private finite copy-stage operation/checkpoint labels, including the final
  archive check. Preserve the existing public error code, text and cancellation
  classification; retain no raw upstream error, identifier, address or config.
- Preserve precondition, stable observation, prefix, rendering and archive order.
  Only an empty archive invokes the existing pre-copy guard, copy and post-copy
  guard. Copy failure still short-circuits the post-copy guard. Exact replay adds
  no copy or explicit effect guards. The final stable, prefix and archive checks
  remain in their original order, as does config-body clearing.
- Make the test-only decorator explicitly implement all eight existing typed
  stage methods. Each delegates once with unchanged arguments, return value and
  error. A compile-time interface assertion prevents silent promotion of a future
  interface method.
- Preserve the earliest failed delegation even when its error is unknown. Clamp
  method names to a finite set. Retain successful copy completion across later
  delegations and report only a generic outer-refusal category when a stage method
  succeeded before a later coordinator refusal. This cannot distinguish binding,
  append or outer guard failure without further evidence.

No production predicate, authority check, SQL/protected-history schema,
coordinator/recovery behavior, factory wiring, module, CI workflow or deadline is
changed. Repeated stable checks within one method may still share the same finite
checkpoint; the labels do not promise a unique invocation identity.

## Baseline and preserved failed attempts

All artifact paths below are under wrapper workspace
`temp/m3-stage-copy-diagnostics-20261010/`. The baseline and RED wrappers pin the
listed relevant source files and module files; these early records are not a
complete repository source manifest. Final acceptance uses the complete Go/module
inventory. The local selected toolchain is official Go 1.26.7 on Windows, matching
the hosted Go version without changing global configuration or module files.

The pristine baseline used `GOTOOLCHAIN=go1.26.7`,
`GOFLAGS=-mod=readonly -buildvcs=false -p=1` and the artifact-local build cache:

```text
go test ./internal/generatedingress -run '^(TestGatewayRebindStageConfigCopyAppendsAndFreshManagerReplays|TestLiveGatewayRebindRuntimeTypedStageDiagnosticDriverRetainsFirstFailure|TestGatewayRebindTypedStageDiagnosticSanitizesUnknownError)$' -count=1 -json -timeout=20m
```

`worker-pristine-baseline.result.json` records all three exact parents passing,
zero skips/failures, exit 0, unchanged pinned inputs, 4.874 seconds package time
and 66.813 seconds wall time. Its raw JSON SHA256 is
`422bc7b73d8e10851697760d3d243997f02b15e29dc0dc081254f277c576a5eb`.

Two initial RED attempts are retained as fixture-selection failures, not proof of
the intended stable-guard boundary:

1. `worker-red-copy-stage-guard.*` selected an invalid phase and checked the
   diagnostic before guard reachability; its generic precondition refusal did not
   prove the stable guard was reached.
2. `worker-red-copy-stage-guard-controls.*` moved reachability controls first and
   exposed zero guard calls. The fixture was then corrected by selecting the
   canonical stage-config-intent phase rather than a positional record index.

The corrected fixture was source-reviewed before execution. Its exact command was:

```text
go test ./internal/generatedingress -run '^TestGatewayRebindTypedStageRuntimeCopyStageConfigRetainsStableGuardDiagnostic$' -count=1 -json -timeout=20m
```

`worker-red-copy-stage-guard-p7-controls.result.json` records the meaningful RED:
one rejecting guard, zero commands/effects, and failure solely because unchanged
production discarded the private nested checkpoint. Go exit was 1, with no skip
and unchanged pinned inputs (18.058 seconds package, 26.968 seconds wall). Raw JSON
SHA256 is
`c078d27e63cbcbd6414683a00fac38a28f5915fe5f3e4c0f1e33aa9af21126bb`.

The first implementation compiled with `go test ... -run '^$'`, recorded in
`worker-compile-diagnostic.result.json`; it executed no behavior tests. Subsequent
review caught that retained copy success was missing from the outer diagnostic
snapshot/log. That wiring omission was corrected before final behavior checks.

## Final verification

The selected behavior inventory has 12 parents and seven nested subcases. There
are 17 terminal leaf paths only when the ten parents without subcases are included
as leaves; this must not be described as 17 nested subcases. Exact anchored
selectors, expected names, platform arguments and source manifests are retained in
the command/result receipts.

| Receipt | Actual outcome |
| --- | --- |
| `worker-final-verification-r2.result.json` | Whole native run failed: 11 parents and seven subcases passed; the pre-cancelled-context test expected `post_effect_guard` but correctly received `first_read`. No skips or source changes. Package 150.083 seconds; wall 206.844 seconds. |
| `worker-final-verification-r3.result.json` | Corrected native parent passed, package 18.011 seconds. Cross compilation and actual WSL execution passed the full 12-parent/seven-subcase selection; WSL wall 15.047 seconds. All 794 tracked/untracked Go and module pins remained unchanged. |
| Same r3 receipt | Read-only `gofmt.exe -d` output empty; `git diff --check` passed; `go vet ./internal/generatedingress ./cmd/hostd` passed; four-command production build passed. |
| `worker-final-logging-delta.result.json` | After the final test-only nil-error logging change, the exact extraction parent passed once on Windows and once in actual WSL, with no skips/failures. Cross compilation, formatting, diff check and scoped vet passed; all 794 Go/module pins remained unchanged. Native wall 65.782 seconds; WSL wall 4.109 seconds. |

The r2 failure is retained as a real test assertion failure. Security and QA
confirmed that a context cancelled before entry, with a successful initial guard,
fails the first stable read. Only the expected private label was corrected;
`DiagnosticCancelled`, public text/unwrap and no-effect assertions remain. The
11 passing native parents were not rerun merely to replace the failed whole-run
receipt. The replacement full WSL selection covers the corrected source.

Raw SHA256 values:

- r2 native: `929c49562e9221054dd985b7a33190952ffaaa691b956a5c1377735373bb2f37`
- r3 corrected native: `91bc23be4bf38d36793951acf00a549c6e0b47795503599237fcaf4ece284e26`
- r3 actual WSL: `e8d901a5cba1030fbdd45c727eafe09e70a74f5a9af3818ce4249aefebae3be3`
- r3 result: `b2de389249e846dbecdeaec38193d6edfa7a74ff2d7494c971ec002e248ad59d`
- Final logging native: `5764d27c9f300f4e161e638deec94b85891acaf21826c1095cb18c7ddc286ca5`
- Final logging actual WSL: `5758486b1dffbe11e6ec659c3bc3c6c81a043bc1affdf23571081c3373543b15`
- Final logging result: `ee9739f69b11b5e5994adf6854ccf16d5a6234cc6da5d83be67d1f0d4c36e2a0`

The native r2 command used Go's default ten-minute package timeout. The r3
replacement native parent and actual WSL command explicitly used 20 minutes.
The configured but unexecuted r2 WSL command used 30 minutes; it is not an
execution result. No repository test, CI or production timeout changed. The
production build command was:

```text
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

Two verifier preflights failed before launching Go: a static scan omitted the
third file containing the existing replay parent, then a generated quote matcher
was incorrect. Those zero-Go attempts remain in
`worker-final-preflight-blocker*.json`. The final runner used the explicit
source-reviewed inventory and assessed actual emitted test outcomes.

Static CI mapping covers all seven new parents and all 18 parents in modified
test files exactly once through the existing complementary partitions. CI and
workflow blobs are unchanged. Initial static-parser CRLF/digit handling errors
are retained separately; they are not test failures or a change to the filters.
The final mapping is `ci-selection-delivery.json`, SHA256
`0001e07ef73ed8d31780db5d5db0f4dd6ae7d3386d9eb7d7685c14ea81675999`.

The last native command was:

```text
go test -json -count=1 -timeout=20m -run '^TestLiveGatewayRebindRuntimeFailureDiagnosticUsesBoundedSnapshots$' ./internal/generatedingress
```

Its Linux binary used the same anchored parent selector with `-test.v`,
`-test.count=1` and `-test.timeout=20m`, executed from the package directory in
actual WSL. Only test logging and its extraction control changed after the r3
matrix; the production source and production-build inputs remained identical.

Final SHA256 source pins:

| File in `internal/generatedingress/` | SHA256 |
| --- | --- |
| `gateway_rebind_typed_stage_driver.go` | `1e8bcf3ef54e692b3c18fd5a75c69716b39ee7a75f4e42a85a0e9ad5a7127efd` |
| `gateway_rebind_typed_stage_diagnostic.go` | `80715a1030c8920208352682e28651dc8446a033f3149da3eb9dcc396f072689` |
| `gateway_rebind_runtime_live_test.go` | `85907008971e2b08b7412186083f3d830a628744d89a1ca30a5fc0e498e0dcfd` |
| `gateway_rebind_typed_stage_diagnostic_test.go` | `df9813e62bfa54a92971d3b793329f72b1fc48873fb703e4fd98424557db204f` |
| `gateway_rebind_typed_stage_copy_diagnostic_test.go` | `4988e031fe15307b1d9efb9f041e1f891ae7c9b7a44fefc0f287ef8c31bc4c9b` |

Security source review accepted the finite diagnostics and unchanged runtime
ordering, including the final nil-error logging delta. Independent QA accepted
the reconciled native/WSL results and preserved failed receipt in
`qa-final-assessment.md`, SHA256
`695173767d55b716f5a5e1715e99cfba6a454501c361f5ed28888bf719599a60`.
The earlier `source-review.json` remains as its original source revision record;
the final source disposition is recorded separately in the evidence folder.

Documentation workflow/build/accessibility are run by the root task after this
page is frozen, using its existing installed Node/pnpm toolchain. Their retained
receipt is the source of the result; this page does not preclaim an unrun check.
The planned receipt is
`temp/m3-stage-copy-diagnostics-docs-check-normal-user-20261010/result.json`, for
`pnpm --dir docs check:workflow`, `pnpm --dir docs build` and
`pnpm --dir docs check:accessibility` through the installed Node/pnpm entry point.

## Remaining acceptance

Local tests use controlled runners and real local protected/SQL fixture setup as
specified by their existing helpers. They do not execute Docker here. The actual
typed commit and rollback Docker journeys remain required hosted gates. Existing
private component successes do not establish the full supported controller flow,
the full real-process crash matrix, or physical second-device LAN acceptance.
No publication, merge, deployment or production activation is performed by this
local unit.
