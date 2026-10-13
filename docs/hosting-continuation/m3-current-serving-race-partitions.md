# M3 current-serving race partition repair

Date: 2026-10-13. Base: `a39193ea479168945f035225fe5efb560b79f4b3`.
Status: local selection verification passed; hosted durations for the new groups
remain unverified. This does not establish M3 completion.

## Observed failure

PR #150's [current-serving race job](https://github.com/tyhuang9/rig/actions/runs/38459223489/job/115509754150)
hit its existing 32-minute test-binary limit. It started 66 parent tests and
completed 65 successfully. No parent assertion failure or data race was reported.
`TestGatewayCurrentTypedEmergencyRetainsRepeatedCommitOwnership` had been running
for 24 seconds when the suite alarm fired; its stack was processing protected
history and canonical digests. That stack does not prove a deadlock or a complete
individual test result. Two other selected parents had not yet started.

The tested source was `1ad62a9f9be7e981c3b9516b682fdea4102dcb03`, merged with
`b591f042c9569742ee66f45863cb3ac938fb5b75` as
`fbacec2d12f4f264adf4db7cdb45ccf400779949`, tree
`a64ddc08f16bf25b1e4ab790853499bb5962ef56`. Raw log SHA-256:
`6407c0137337ecc4004cfc82aa48c45a05a07d90c10c6a5fbdd37b4d29da4421`.
The original log, metadata, inventory and merge proof remain under
`temp/m3-pr150-readiness-ci-20261013/job-115509754150.*` in the workspace.

## Narrow change

Split the existing current-serving selection into three complementary groups:

| Group | Linux/Windows parents | Parents with completed hosted timing | Sum of completed times |
| --- | ---: | ---: | ---: |
| `current-serving-emergency` | 24 | 23 | 434.65 seconds |
| `current-serving-predecessor` | 4 | 4 | 456.89 seconds |
| `current-serving` (remaining cases) | 40 | 38 | 1004.35 seconds |
| Total | 68 | 65 | 1895.89 seconds |

These are measured parent durations from the failed old group, not predictions
or acceptance results for the new groups. The uncompleted parents are:

- `TestGatewayCurrentTypedEmergencyRetainsRepeatedCommitOwnership`
- `TestGatewayCurrentImageMetadataRequiresStableObservation`
- `TestGatewayCurrentTerminalImageMetadataRequiresNamedIDAgreement`

The shared runner selects emergency names matching
`^TestGatewayCurrent(Typed|Native|Startup|Terminal)Emergency` and predecessor
names matching `^TestGatewayCurrentPredecessor`. The existing current-serving
fallback excludes both families, retaining all other current-serving names and
future names not assigned to a specialized group. Existing LAN and runtime
exclusions remain unchanged. Whole subtest trees stay with their parent.

The generated-runtime Linux workflow, repository Linux workflow and Windows
workflow add the same two rows. Linux retains race detection; Windows retains
plain execution. The runner keeps `-count=1`, the 32-minute timeout, the nonempty
PASS guard and pipeline failure propagation. Workflow timeouts, aggregate gates,
permissions and failure policies are unchanged. No application or test behavior,
runtime safety check, authority, secret handling or protected history changes.

## Local verification

The new branch starts directly from the published base, with no branch integration
or history rewrite. Pinned Go 1.26.7 uses `GOTOOLCHAIN=local` and
`GOFLAGS="-mod=readonly -p=1 -buildvcs=false"`.

Before editing, `go test -c` compiled the ingress test binaries for Linux and
Windows. Running their actual `-test.list .` discovery produced 983 Linux and
981 Windows parents. Linux discovery executed through WSL. All 890 Go/SQL/module
inputs remained frozen. Linux compilation/discovery took 11.266/4.078 seconds;
Windows compilation/discovery took 11.969/0.032 seconds. These are compilation
and discovery results, not execution of the complete test suite.

Pinned offline/frozen documentation installation and the baseline workflow,
build and accessibility checks passed before edits.

The actual Bash runner was invoked with an exported `go` argument-capture function
that deliberately exits 97. Before editing, all 38 race/plain cases preserved
that failure. After editing, all 42 cases did so. Both versions rejected an unknown
suite, invalid mode and extra argument. Shell syntax passed on canonical LF input.
The initial sandbox attempt failed to create an MSYS runtime object; the same
baseline check passed outside the sandbox. No test execution is claimed from the
capture function.

A Go regular-expression audit applies the captured arguments to both actual
compiled inventories in race and plain modes. All four comparisons passed:

- Every parent is selected exactly once; no group is empty.
- The three groups' union equals the original 68-parent selection.
- All other groups' arguments and selected parents are identical to baseline.
- Thirteen synthetic future-family and boundary names have their expected owner.
- Deliberately missing and duplicated groups are rejected.
- Parsed workflows differ only by the two intended matrix additions each.
- The runner's execution and nonempty-result-check suffix is unchanged.
- `git diff --check` passed and all 890 source inputs remained unchanged.

Independent source/QA review found no actionable issue. CodeRabbit approval is not
claimed. Final documentation checks and source receipt validation are recorded
with the publication evidence.

Local receipts are under `temp/m3-current-serving-race-20261013/`, including
`baseline.json`, `baseline-unrestricted-capture/result.json`,
`final-capture/result.json`, `final-audit.json` and `hosted-timings.json`.
The audit retains exact commands, selections, source hashes and negative cases.

## Remaining gates and rollback

Local WSL lacks the native Go/GCC toolchain for Linux race execution; local Docker
is unavailable. Hosted CI must execute every new group, preserve the full test
inventory and prove completion within the unchanged limit. More groups add job
startup/compilation overhead. Future growth may require another measured split.

The separate Docker forward-cleanup failure remains under investigation on the
parent PR. This CI repair does not establish runtime acceptance. Full-controller
restart and physical second-device LAN qualification remain outstanding.

If necessary, revert this focused runner/matrix change through a reviewed commit;
that restores the previous grouping and its known timeout risk. There is no
production runtime rollback or data operation. Publication does not authorize a
GitHub merge or deployment.
