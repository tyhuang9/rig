# M3 runtime cleanup follow-up evidence

## Hosted baseline

Draft [PR #149](https://github.com/tyhuang9/rig/pull/149) published source
`0cfb94f9e241dde136b343e1a95ce416cc111276` against
`cb83fa480c16eec33c8b38e9846ae421139bbce0`. The observed GitHub test merge
`1a0e6b6773501511e480fd16a8d115ab7d5594ac` has those two parents and tree
`12f6f872831f34c7d8f6e48bda95bf6fda6daf50`, matching the local source tree.
These results apply to that revision, not to this follow-up.

| Gate | Actual result |
| --- | --- |
| [Docker forward commit and replay](https://github.com/tyhuang9/rig/actions/runs/38452402904/job/115486378381) | Failed after 146.26 seconds. Exact terminal and fresh-manager replay succeeded, then initial cleanup attestation refused. Resources were retained; independent cleanup consequently failed. |
| [Docker rollback and replay](https://github.com/tyhuang9/rig/actions/runs/38452402904/job/115486378323) | One parent ran and passed, no skips; independent cleanup passed. |
| [Stopped successor stage](https://github.com/tyhuang9/rig/actions/runs/38452402904/job/115486378556) | One parent ran and passed, no skips; independent cleanup passed. |
| [Windows ingress remainder](https://github.com/tyhuang9/rig/actions/runs/38452403192/job/115486379616) | 287 parents: 262 passed, 25 expected opt-in/platform skips, no failures. All six TLS controls and diagnostic parents ran and passed. |
| [Controller lifecycle race](https://github.com/tyhuang9/rig/actions/runs/38452403007/job/115486379291) | 26 parents ran and passed without skips. |
| [Controller remainder race](https://github.com/tyhuang9/rig/actions/runs/38452403007/job/115486379049) | 90 parents ran and passed without skips. Together the two lanes cover all 116 controller parents. |
| [PostgreSQL and repository remainder](https://github.com/tyhuang9/rig/actions/runs/38452403007/job/115486378744) | PostgreSQL integration and outage race steps passed. Repository remainder failed `TestReadyObserverUsesClosedProbeAndCacheStates`, with `states=[cached_success probe_failure cached_failure]`. This is being handled separately. |

Raw logs, metadata, source-merge proofs and JSON inventories are retained locally
under `temp/m3-pr149-hosted-acceptance-20261013/`. Log SHA-256 values:

| Job | SHA-256 |
| --- | --- |
| 115486378381 | `d379d31bb9d6535f8432edd209bec97e9f09193d211410113f3a6461eeccde5d` |
| 115486378323 | `7d8e9b59e5caf16a737f9cd575296012298ee6f967a52af6ff95160ef305a089` |
| 115486378556 | `855ffdd8aec4ff0f78aafcc4e5104900ebf6fc42577d505716e0c56e91a300b3` |
| 115486379616 | `8b87ad8670243cda2f29858a6fff60aa781d477e843d20d22623b216506bd83d` |
| 115486379291 | `87af4d2d68bc0594f66445131621efe402cbd779b04e27bd14a3bee55cb12069` |
| 115486379049 | `1c3e21ba1b8f28faf8afa882ce3736409c52e8c6eea4af20ba0d67122b8c8ce2` |
| 115486378744 | `b26824d883e42e2e8e7067e01d8d7ebaaf1cb8018afeec4e337fda6ae4fe46a8` |

## Cleanup regression

The cleanup comparator required the entire final-container label map to equal
Rig's expected labels. Docker images may supply unrelated OCI metadata. The
test-only comparator now uses the existing exact Rig-label validator, preserving
every required Rig label and rejecting missing, changed or unexpected Rig
labels. Other resource, authority, consumer, listener and effect-order checks
remain unchanged. This is a later deterministic mismatch; it does not explain
the earlier initial attestor refusal.

The new regression first failed only its foreign-image-metadata positive case:
four parents and 13 descendants ran. After the comparator repair, those tests
and the real composed generic-attestation baseline/metadata cases passed:
five parents and 15 descendants, no skips, with all 888 Go/SQL/module inputs
unchanged through compilation and execution. The composed test requires exact
stable-serving target, entry into predecessor observation, no new effects and
unchanged SQL, protected history and admission latches.

Execution used Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, Linux amd64 with CGO disabled:
`go test -c -o <binary> ./internal/generatedingress`, exact `-test.list`, then
`go tool test2json -t -p github.com/hostd/hostd/internal/generatedingress`
wrapping actual WSL execution with `-test.v=test2json -test.count=1
-test.timeout=10m -test.run <anchored parent list>`.

An initial sandbox WSL launch failed with `Wsl/E_ACCESSDENIED` before any tests.
The unchanged compiled binary executed outside that sandbox to establish RED.
Both receipts are retained. RED log SHA-256:
`b6431a5b644434e68a7fe602fd4845c3d9d5e3a2948cd150b9b7953020eaa759`.
Initial combined GREEN log SHA-256:
`e9281c93ad69dde1bbc9ed9b40f276c5a9a7ebbe3c539e8e07413ce78c6c9449`.
Raw local receipts: `temp/m3-cleanup-proof-20261013/`.

## Initial-attestation diagnostics

The live cleanup still calls the actual generic manager attestor exactly once.
A private test-only wrapper counts entries and failures in the managed driver,
predecessor observation and runtime observation. Its error text contains only
fixed field names, booleans and integers. It prints no raw delegated error,
resource identifier, command, configuration, credential or endpoint. The exact
installed driver is restored with `defer`; unknown driver/runtime types pass
through without instrumentation. There is no retry, alternate attestor, repair
effect or relaxed predicate.

The composed regression now exercises this actual wrapper on the real managed
driver. Seven diagnostic subcases additionally check successful/failing driver,
retirement and runtime delegation, and restoration after manager refusal.
The predecessor audit found that its existing container validator already permits
unrelated OCI labels. The later raw-runtime snapshot's stable/stopped outcome
expectations also match the production observer. Neither audit established the
cause of the initial hosted refusal, so neither production path was changed.

## Final local test gate

The frozen final source gate passed six parents and 22 descendants without
skips or missing tests. All 889 Go/SQL/module inputs were unchanged through
compilation, exact discovery and execution. Compile: 9.625 seconds; WSL test
execution: 21.500 seconds. Parents:

- `TestGatewayRebindRuntimeCleanupRejectsUnownedResources`
- `TestGatewayRebindRuntimeCleanupStopsOnUncertainEffects`
- `TestGatewayRebindRuntimeCleanupRemovesOnlyExactResourcesInOrder`
- `TestGatewayRebindRuntimeCleanupImageMetadataDoesNotGrantAuthority`
- `TestGatewayRebindRuntimeCleanupGenericAttestationAcceptsComposedCommittedServing`
- `TestLiveGatewayRebindRuntimeCleanupAttestationDiagnosticDelegatesWithoutMutation`

Final compiled binary SHA-256:
`1f2fa3929525ae5ac457a4844b7ab7f9bdb54357be9a4f883bc752f614693653`.
Final test log SHA-256:
`92ff9db1efed9e39435c142d1a4fd7d4956593d4e4dd7abca0b77acfd508cdd3`.
The exact command and per-file hashes are retained in
`cleanup-final-wsl.command.json` and `cleanup-final-wsl.result.json` in the
local receipt directory named above.

Canonical LF formatting of all four changed Go test files, `git diff --check
HEAD`, and `go vet ./internal/generatedingress` passed. Vet took 48.906 seconds;
all 889 source inputs remained unchanged. Independent code/security review found
no actionable issue in the follow-up scope. No CodeRabbit approval is claimed.
Only test files and this evidence page change relative to the published baseline;
production CLI builds are covered by that unchanged baseline, not a claimed new
build run. Actual Docker and hosted race acceptance remain pending for this source.

## Limits

Local Docker is unavailable (`desktop-linux` engine named pipe absent).
Passing mocked Docker composition does not establish actual Docker acceptance.
The earlier live initial attestation refusal remains unproven. The next hosted
run must identify that boundary and pass forward/replay plus independent cleanup.
M3 is incomplete. Supported full-controller restart recovery and physical
second-device LAN qualification remain later gates. No managed database or Neon
provisioning, production activation, deployment or GitHub merge is included.

## Later hosted evidence: retirement predicate refusal

The [PR #150 forward job](https://github.com/tyhuang9/rig/actions/runs/38459223388/job/115509753049)
ran source `1ad62a9f9be7e981c3b9516b682fdea4102dcb03` against baseline
`b591f042c9569742ee66f45863cb3ac938fb5b75`. Its exact test merge is
`fbacec2d12f4f264adf4db7cdb45ccf400779949`, with tree
`a64ddc08f16bf25b1e4ab790853499bb5962ef56`. This run passed exact terminal
commit and fresh-manager replay, then failed initial cleanup attestation:

```text
wrapped=true manager_refused=true driver_entries=1 driver_failures=1
retirement_entries=2 retirement_failures=1 runtime_entries=1 runtime_failures=0
stable=false at_target=false
```

The parent failed after 150.22 seconds; independent cleanup failed and resources
were retained. Raw log SHA-256:
`d85ab579f58d738efe20f2a52f618d4de1d1f20338ab619b3fd391035a6499c0`.
Metadata, merge proof, log and exact RUN/FAIL inventory are retained in
`temp/m3-pr150-readiness-ci-20261013/job-115509753049.*`.

The first retirement observation passed. The raw runtime observation returned
successfully. The second physical retirement delegate then refused. The final
`stable`/`at_target` flags follow that refusal; they do not show that the raw
runtime observation returned unstable. The four remaining physical boundaries
are the opening inventory, profile listeners, local listener, and closing
inventory. Surrounding SQL/history rereads are outside the counted delegate.

The listener methods may internally call the concrete `exactCurrentServing`
runtime observer; those nested calls bypass the top-level diagnostic runtime
counter. A successful counted runtime observation therefore does not rule out a
later nested probe refusal. Runtime observation creates no transient container or
network endpoint. No production defect or behavior repair is established yet.

### Predicate diagnostic baseline

Before extending the existing private cleanup wrapper, clean local source
`c8b2a094948169de0ec3c7f121c34ceae91911af` passed two exact parents and nine
descendants: composed baseline/foreign-image-metadata attestation, plus the seven
existing delegation contracts. There were no skips or missing tests. All 890
Go/SQL/module inputs remained frozen through compilation, discovery and actual
WSL execution using the Go 1.26.7 environment and commands recorded above.
Compilation took 3.437 seconds, discovery 3.750 seconds, and execution 21.156
seconds. Binary SHA-256:
`a44c586888cc1be33366bfc379b6e87f53aa7499935b2f1f3dc3b7e0976e0475`.
Test log SHA-256:
`ba1f112c1a19f6becb08363fc1c71e33e818c947cd8431a717370438ad09cca7`.
Exact receipts: `temp/m3-retirement-predicate-proof-20261013/retirement-baseline-wsl.*`.

The test-only extension records which original predicate returns false, preserving
every original argument, one delegation and the exact result. Each predicate has
entry/failure counts and a first-failure call ordinal. The first failed predicate
uses a private closed enumeration; diagnostics retain no raw context, action,
profile, port, guard, upstream error or authority value. The existing concrete
runtime embedding and optional interfaces remain intact. No retry, changed
ownership predicate or runtime authority is added.

### Predicate diagnostic final verification

The final formatted source passed both parents and all 15 descendants, without
skips or missing tests. Exact RUN/PASS inventories matched discovery. The existing
composed baseline and foreign-image-metadata cases require calls to all three
physical predicates, no reported failures, unchanged physical effects, SQL and
protected history. Six additional contract cases cover both returned booleans
for each predicate, exact argument and guard forwarding, one delegate call, and
finite counters. The seven prior delegation cases remain included.

All 890 Go/SQL/module inputs remained unchanged. Compilation took 8.672 seconds,
discovery 4.187 seconds, and WSL test execution 21.796 seconds. Binary SHA-256:
`0fefd52691604b6f00c84c30cdb8421449d4266997eedffab87c0a36ad86f19d`.
Test log SHA-256:
`1a7ee2c726d0f3468016460520ed94d40d6c100442cdcb66bc3cc5f7480c79f5`.
The exact parent/descendant inventory, commands and per-input hashes are in
`temp/m3-retirement-predicate-proof-20261013/retirement-final-wsl.*`.

Canonical LF formatting of both changed Go test files, `git diff --check HEAD`,
and `go vet ./internal/generatedingress` passed with unchanged inputs; vet took
45.172 seconds. Pinned offline documentation installation passed in 0.875 seconds.
Documentation workflow, build and accessibility checks are required before
publication. Only two test files and this evidence page change; no new production
CLI build or local Linux race acceptance is claimed. Actual hosted predicate
evidence and successful complete Docker cleanup remain unverified for this
extension. M3 remains incomplete.
