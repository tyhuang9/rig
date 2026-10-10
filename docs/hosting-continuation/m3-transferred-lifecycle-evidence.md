# M3 transferred sharing lifecycle

Recorded 2026-10-09. Branch `feature/hosting-m3-transferred-lifecycle` starts
from `b3bdc230bdd9cfc49ad388b174d13b26e1854869`. The previous intent-crash branch
and reviewed runtime candidate remain separate. This record concerns a local
integration test; it does not authorize publication, integration or deployment.

Status: the complete new lifecycle passed on Windows and Linux, the strict
command checks passed on Linux, and the retained second-generation commit and
rollback regression passed. Final static/documentation gates are recorded below.

## Required behavior and coverage gap

[M3 cutover acceptance gate 7](m3-rebind-cutover-contract.md#acceptance-gates)
includes a transfer lifecycle: a new native grant after rebind, disable of a
transferred old grant, restart projection and a later rebind while the raw grant
digest/profile remain unchanged. This unit addresses that portion of gate 7;
the gate also requires startup admission checks and handling an old NIC that
returns after commit. Separate existing tests cover real-SQL disable finalization and two
concrete rebinds, but the former seeds its withdrawal state and the latter
carries the same original grant through both moves. Neither establishes the
whole post-rebind sharing lifecycle against the same retained state.

The new sequence uses the existing concrete composition and retained
multigeneration backend, real repository authorization and transitions, and the
public grant/disable methods. It must prove:

1. Complete the first rebind and retain its exact source/transfer authority.
2. Remove the transferred old grant through actual withdrawal, proof and SQL
   finalization; do not preseed a withdrawn state or a successful clear ACK.
3. Approve and apply a distinct native grant on the selected successor profile.
   Its binding initially has no transfer; retired old authority cannot operate
   on it or produce effects.
4. Recover through a fresh Manager, perform a concrete later rebind, and read
   the new grant's actual later transfer from SQL.
5. Preserve old raw grant/profile/proof and transfer history. Fresh replay must
   preserve all retained state and issue no mutation. A crossed proof must be
   refused without rewriting that history.

A bounded test-backend extension is needed for the current physical driver's
exact `exec --user 0:0` config-copy/rename form. Parsing must preserve owned
container identity and reject malformed or wrong-ID commands. It must not turn
the backend into a permissive successful-command stub.

## Scope and invariants

The permitted implementation scope is one new test file and narrowly needed
extensions to the two existing composition/backend test helpers. Existing
production adapters, guarded SQL, immutable history, public APIs, factories and
CI remain unchanged. No production defect is assumed before a failing result.
External databases remain application-owned and configured through scoped
runtime secrets; no managed database or Neon work is involved.

The backend simulates physical Docker observations and effects. Real SQLite,
protected files, concrete adapters and public lifecycle methods improve the
composition evidence, but do not prove actual Docker, an HTTP client journey,
a killed hostd process, Linux race behavior or physical second-device LAN.

## Clean baseline

Both baseline selections ran on clean `b3bdc23` before any source edit, using
Go 1.27.0 windows/amd64, `GOTOOLCHAIN=local`, read-only module/VCS flags and the
existing workspace Go cache. Source hashes stayed unchanged. The independent
SQL selection ran in the second Go slot; no more than two Go commands ran at
once. Both processes completed and were reaped before implementation began.

```text
go test ./internal/generatedingress -run '^Test(GatewayRebindConcreteCompositionSecondGeneration|GatewayCurrentLANRecoveryFinalizationCommitsRealSQLAfterRebind)$' -count=1 -json -timeout=25m
go test ./internal/appaccess -run '^(TestAppAccessGrantClaimLifecycleAuthorizationAndDisableFence|TestAppAccessDisableClaimCommitsExactReleaseAfterProof)$' -count=1 -json -timeout=5m
```

| Selection | Actual result |
| --- | --- |
| Generated ingress | Two parents and two subcases passed; zero failures/skips; exit 0; package 834.677 seconds. Real-SQL removal took 30.26 seconds, second-generation commit 344.48 seconds and rollback 459.58 seconds. |
| Application access | Two parents passed; zero subcases/failures/skips; exit 0; package 1.359 seconds. This command also used `-p=1` in `GOFLAGS`. |

Exact environments, complete outputs, source hashes and terminal results are
retained under `temp/m3-transferred-lifecycle-20261009/`. The generated-ingress
prefix is `baseline-generatedingress`; the application-access prefix is
`baseline-appaccess-root`. Their JSON output SHA-256 values are respectively
`7A837B2B340E3B88658A844A5D76E724921135427378587EA166C90C17A038E6` and
`0FC5C631206D517DA246606D3845288BB59948DC12EA925190B13D6A809CCA9D`.

## Preserved implementation diagnostics

The first compile-only check of the draft failed before any behavior test ran:
the new lifecycle fixture referenced `input.Attempt`, which is not a field of
`gatewayRebindCommitInput`. The original command, empty standard output and
compiler error remain under the `pre-freeze-compile` prefix in the artifact
directory above. The error file SHA-256 is
`AA594036503BC99B7195D6ACDC06BF87E3192D51839FAABB40738A0316AB108A`.
This failure is a draft fixture API mismatch, not evidence of a production
regression or a completed acceptance run.
The original process is no longer live, but its launcher did not retain the
process handle's terminal exit code. The compiler output is preserved; no
specific exit code is inferred for that attempt.

A second compile-only attempt after source review found another test assertion
using a nonexistent field, `GatewayRebindAllocationTransfer.AccessRevisionID`.
The transfer type stores allocation/grant identity; access-revision identity is
checked through the binding reference and retained roster. The narrow correction
must retain those assertions. This attempt is preserved under
`review-compile-generatedingress`; its compiler-error SHA-256 is
`6BB55DA18F56498C9FB4B4E73D27C0CB9D3A9C6266C48C3653EFA656987D0138`.
No behavior test ran. Its original launcher also failed to preserve a terminal
exit record, so no specific exit code is claimed. Subsequent wrappers must await
the subprocess and record its actual exit within the same execution session.

The first focused behavior run then failed in
`TestGatewayRebindMultiRunnerCurrentCommandRefusal` while exercising its valid
reload control: the backend refused an invalid autosave configuration. The test
had taken an uninitialized stage body and appended a newline. The shared
initializer intentionally leaves that body empty until actual copies occur.
This is a command-test setup defect; the full lifecycle has not run. The saved
`fast-current-command-refusal.result.json` records exit 1, one failed parent,
zero skips, package time 18.614 seconds and unchanged source hashes. Output
SHA-256: `8F8A658E70D19977DDDE3BF707111066B3FB1C696CD0D11501D35C0CCA760E0B`.
The correction must use the existing renderers to build valid physical starting
state for this command-only test, preserve real lifecycle copy behavior, and
reject malformed candidate configuration before altering live/restart state.

The first full native lifecycle attempt also failed before the first rebind
completed, returning `route_reconciliation_required` at the first-commit
assertion. It reached none of the subsequent lifecycle or proof-negative cases:
one failed parent, no subcases/skips, exit 1, package 105.018 seconds and wall
106.188 seconds. Source hashes stayed unchanged. The artifacts are under
`native-transferred-lifecycle`; output SHA-256 is
`7C25F2091969762EC8B6078DC513191E57F655B82B3D968F33F23B0B19918EAB`.
This was not a timeout. Investigation identified a changed test-helper TAR
layout: direct-file copies now included a leading directory header, while the
production reader requires the first header to be the regular file. The bounded
fixture correction and focused results are recorded below; production validation
remains unchanged. Full lifecycle acceptance still requires a new passing run.

The archive-corrected native attempt reached and passed the first-rebind
assertions, then failed in public `DisableGatewayV2LAN` with
`route_reconciliation_required`. The whole parent remains failed: exit 1,
package 115.772 seconds, wall 117.500 seconds, no subcases/skips and unchanged
source. Prefix: `native-transferred-lifecycle-archive-fix`; output SHA-256:
`A58FDC6A31089CA7C79CDB217FE10E8A6C35F1E76F542D5C7F8368663062D36E`.
Review found that the fixture lease permits only `prepared`, whereas the actual
controller also permits `withdrawing` and makes withdrawal idempotent. That
fixture mismatch needs correction, but it is not yet established as the cause
of this first disable failure. Bounded callback/state diagnostics will identify
the refusing boundary. No production defect or repair is claimed.

After the narrow lease correction, the diagnostic retry still failed at disable:
exit 1, package 113.525 seconds, wall 119.062 seconds, no subcases/skips and
unchanged source. Prefix: `native-transferred-lifecycle-disable-diagnostic`;
output SHA-256:
`B64D86DA17F42DA899C761DC6D852C57739F63035A51B14CC4A846BFBAFE4E4E`.
The recorded authorizer/revalidation/withdraw counts were `1/2/1`; SQL was
`withdrawing`, selected protected revision was 2 with a `lan_disable` pending
operation, and the simulator's effect count grew from 12 to 18. All three
diagnostic state reads succeeded. This localizes the remaining refusal after
authorization and physical effects; it does not establish a successful disable.
The lifecycle test hash for this attempt is
`F18F4ECC9497B8DCC67C0098A28B9D5274AC568804E6E7D1ED8E678824DD7DB4`;
the two helper hashes match the archive-correction table below.

## Verification so far

### Before the archive correction

An intermediate corrected revision compiled with `go test -c -o <temporary
binary> ./internal/generatedingress`: exit 0, 5.922 seconds, output binary
present, and all three test-file hashes unchanged. This compile-only result is
recorded in `review-compile-retry-generatedingress.result.json`; it executes no
test and does not establish the lifecycle behavior.

After correcting the command-test setup, these native checks passed on the
same frozen source:

```text
go test ./internal/generatedingress -run '^TestGatewayRebindMultiRunnerCurrentCommandRefusal$' -count=1 -json -timeout=2m
go vet ./internal/generatedingress
```

The focused test compiled and executed exactly one parent: no subcases, failures
or skips; exit 0; package 17.085 seconds. It includes valid-command controls,
malformed command/config refusals, configuration-derived proof checks, archive
metadata assertions and unchanged-state checks. Its full output SHA-256 is
`EAC269D1E6D10409D57D6C055CF0DC085BFDC2C60B532DBA21F81B677670A0F7`.
Vet exited 0 in 1.328 seconds with empty output. The saved result prefixes are
`fast-current-command-refusal-retry` and `scoped-vet-generatedingress`.

All three file hashes stayed unchanged during both commands:

| Test file in `internal/generatedingress/` | SHA-256 |
| --- | --- |
| `gateway_rebind_runtime_composition_test.go` | `0C1F8082DC43E314450327DB85199224C6ECBB9DCFB05BA9D9912290DB5393B2` |
| `gateway_rebind_multigeneration_backend_test.go` | `79C45D9EBF5A7146512DF959DCB6BC5824723DB3B3D9093F5F3A18A09796FE47` |
| `gateway_rebind_transferred_lifecycle_test.go` | `2C653860654322B140F273898475F94C96EB2905114BFB31BA3D4D9AB0B71B9C` |

Independent source/security review accepted the bounded fixture changes and
their narrow corrections. The file-mode assertions describe the simulator's
copy/move behavior; they are not a claim that production's stopped-config reader
attests every ordinary file's ownership and permissions.

### Archive correction

Direct-file copies now use a single regular-file TAR entry. Directory copies
retain their directory header and full file census. The focused command test
also calls the production restart reader before current configuration is
introduced and after the active-file move. Production readers are unchanged.

The same focused test command passed on this corrected source: one parent,
no subcases/failures/skips, exit 0, package 16.389 seconds. Its output SHA-256 is
`2BF29BD6F3E2FDB3011455B60B48BA14E0102500A6DA98F433CF68A8A3261CA7`.
`go vet ./internal/generatedingress` passed with exit 0 in 1.593 seconds.
Result prefixes are `fast-current-command-refusal-archive-fix` and
`scoped-vet-archive-fix`. Both recorded unchanged source; root also checked the
saved files directly:

| Test file in `internal/generatedingress/` | SHA-256 |
| --- | --- |
| `gateway_rebind_runtime_composition_test.go` | `2954C441221F6ECFAFAB15BDD8FC579EA41E27EDB60D58BB35B7EADE9C9ED294` |
| `gateway_rebind_multigeneration_backend_test.go` | `79C45D9EBF5A7146512DF959DCB6BC5824723DB3B3D9093F5F3A18A09796FE47` |
| `gateway_rebind_transferred_lifecycle_test.go` | `35CFBC94581456EFF68DC50DBFB3354EF342C61008729D6B66050614F62FA262` |

Implementation and frozen-source verification remain in progress. The final
selection must discover the new lifecycle and strict command-denial cases,
execute them on Windows and actual Linux under WSL, and retain the existing
two-generation test once on the changed helpers. The unchanged SQL baseline
above need not be repeated. Actual durations must inform later hosted CI
capacity; the new parent already matches the existing composition partition
and must not be silently excluded. All failed attempts must remain distinct
from final passing evidence.

### Current probe projection correction and remaining refusal

The aggregate simulated server read the original stage body after a current
configuration reload. It now reads `currentLiveBody()`, retaining the existing
fallback before current configuration is initialized. The focused command
control removes a selected LAN reverse-proxy route and proves that the same
aggregate probe changes from 200 to 404 after the canonical reload, while the
original stage body and pre-move restart body remain unchanged.

`fast-current-command-refusal-live-probe-fix` passed one parent with no
subcases, skips or failures: exit 0, package 16.425 seconds, output SHA-256
`76028A0D70381BA44CA41DEFEEE8CBA677E2A87DD38C2622297E44FA603DDC1D`.
Source/security review accepted this correction, and source hashes remained
unchanged during verification.

The following exact lifecycle retry still failed after disable effects:
`native-transferred-lifecycle-live-probe-fix`, exit 1, package 116.141 seconds,
wall 117.312 seconds. Output SHA-256:
`F5A89F4075222AD88C77B0073246B1D8CA14ABDCC94CA5268728B82C0E2DF00D`.
No expected subcases were reached; none skipped. Authorization/revalidation/
withdrawal counts remained 1/2/1, the SQL claim was withdrawing, protected
revision was 2 with a pending LAN disable, and the recorded effect count grew
from 12 to 18. This preserves the failed acceptance; the focused probe pass is
not a lifecycle pass.

Those two commands pinned composition `2954C441221F6ECFAFAB15BDD8FC579EA41E27EDB60D58BB35B7EADE9C9ED294`,
aggregate backend `6530BBE5DF0C007AE43B93BB2ACE22973E3CD970A24DF04F58F110633AA6AED4`,
and lifecycle `75CC1B4568A0686826B0640C3A58634283536D120FA1865B4961E03623D34DE3`.
The next source investigation found that the simulator's current-command
dispatcher rejects the production endpoint HEAD probe after current config is
initialized. A narrowly validated read handler is under review; no further
lifecycle pass, WSL acceptance or retained second-generation run is claimed.

### Endpoint transport read control

The simulator now recognizes the exact production endpoint HEAD command only
for the retained final container, live configured upstream, attached network
and uniquely owned running alias. Source/security review required uniqueness
independent of endpoint port and prohibited a general curl fallback.

The first fast run (`fast-current-command-refusal-endpoint-head-fix`) passed its
actual `Manager.probeGatewayEndpoint` positive controls but failed the negative
case's retained-state assertion: that assertion reused a snapshot from before
the test deliberately removed the live upstream. Exit 1, package 16.383 seconds,
wall 21.859 seconds, with no source change. Output SHA-256:
`0F8FEBA3AE71EBBF4DB9A4AB2F8ECD161511F2AF383E2F06F47B335E72DBCEA4`.
This failed result is retained. The narrow correction captures a fresh deep
snapshot after each deliberate fixture mutation and immediately before each
rejected command, retaining exact after-state and no-effects assertions. The
full lifecycle retry was not started from this failed fast check.

After correcting the snapshot timing, the focused control passed one parent
with no subcases, skips or failures: exit 0, package 16.149 seconds, wall
21.578 seconds. Result prefix:
`fast-current-command-refusal-endpoint-head-snapshot-fix`; output SHA-256:
`51008177CC87AE186E460BF1353FD73896E79DE3A54D9DD7A46D98DEE43E1C3C`.
Both helpers remained unchanged from the first endpoint-control run; the
lifecycle test hash was
`FDD22BA5BD4F7907F5241E49496B70EC2AEB7EFEAA1CFA3730F5B3C39176639B`.
Actual Manager probe positives before/after reload, exact command refusals,
retained-state invariants and the restored-state control all passed. A new full
lifecycle retry is required; this focused result does not establish that journey.

### Lifecycle now reaches regrant

`native-transferred-lifecycle-endpoint-head-fix` reached the new-grant helper
after successful disable, protected finalization, selected-address withdrawal
and stale-request checks. It then failed at line 171 with `LAN access revision
conflict`. This differs from the earlier physical proof refusals. Exit 1,
package 115.812 seconds, wall 117.000 seconds; source unchanged. Output SHA-256:
`0984D5402FC4BE2FA0695B44ECB73A1F1EDF3CAB2F1B261C62EAF3DA3A80B52F`.

The fixture omitted `ExpectedRevisionNumber` from both its new reservation and
approval input. Both real repository methods compare this against the retained
access head, and the existing disable/re-enable repository test supplies the
prior revision to each operation. The narrow correction passes that pinned
revision and requires the new approval revision to advance by exactly one.
Production optimistic-concurrency checks remain unchanged. This is a failed
whole-journey result with verified progress past withdrawal, not a lifecycle pass.

### Lifecycle now reaches the later rebind

After the expected-revision correction,
`native-transferred-lifecycle-newrevision-fix` passed the preceding disable,
finalization, new-grant approval/activation, serving and fresh-Manager replay
assertions. It failed at line 292 when the later typed rebind returned
`ingress_drift_detected` with an empty commit result. Exit 1, package 148.570
seconds, wall 154.000 seconds; no skips or expected terminal subcases were
reached. Source hashes remained unchanged; output SHA-256:
`2FCA867C941CEB1B267467863A3C64F310277789E1771ABF8E1949B0C5CDA231`.
The lifecycle source hash was
`AA6E99D8E461589FC006144B74B6B5CF23D5EB5D6FB80932813002F8373F29A6`;
both helpers retained their endpoint-control hashes. The next investigation
concerns retained current authority at the later preflight. This remains a
failed whole-journey result; successful earlier assertions do not establish
later transfer or terminal replay acceptance.

The fresh test Manager intentionally omits its predecessor's injected network
observer. This journey then attempts another rebind and therefore needs the
same simulated host census after restart. Without it, preflight queries the
real host network against simulated fixture addresses. The narrow correction
retains that observer closure before reinstalling the backend; physical state,
SQL authority, protected history and production checks remain unchanged.
The lifecycle hash after this change is
`20788877AFC7FC2DA6A89869009D5A9B2B402169849E3DFFE8233C64302218DD`.

Its first execution (`native-transferred-lifecycle-fresh-observer-fix`) failed
before constructing the fixture because the restricted environment denied
creation of the generated builder root. Exit 1, wall 7.281 seconds; unchanged
source; output SHA-256
`59B35C147695EF1F3C7E1459EBAB951FF52BF3822B91CAA6F3D21F1616D1C0BC`.
This environmental failure is retained separately from the subsequent
normal-user execution; it establishes no lifecycle behavior.

### Complete native lifecycle pass

The unchanged-source normal-user retry passed the complete lifecycle and both
`missing_proof` and `crossed_proof` subcases, with no failures or skips. Prefix:
`native-transferred-lifecycle-fresh-observer-fix-normal-user`; retained process
was reaped with exit 0, wall 358.609 seconds, package 357.359 seconds and parent
356.84 seconds. Output SHA-256:
`89C250C5D29A1C7107163ADD501885FC88AE07FD63E96552AC077D7495457AEF`.

The actual public disable/finalization, distinct next-revision grant, serving,
fresh Manager replay, later concrete rebind, exact new transfer, immutable old
history and proof-refusal assertions all executed successfully. This is
Windows execution with the retained simulated Docker backend, real SQL and
protected state. Actual Linux execution and the existing second-generation
regression remain required; physical Docker and hosted acceptance are separate.

### Retained second-generation regression

The existing `TestGatewayRebindConcreteCompositionSecondGeneration` also passed
on the final changed helpers, with both `commit` and `rollback` subcases, no
failures or skips, and unchanged source hashes. Prefix:
`native-second-generation-final`; actual exit 0, wall 775.359 seconds, package
774.108 seconds. Output SHA-256:
`6D96343C5FC8977B5E73E95343360DA996D43848DD9CA1DCC279D2710DAA457A`.
This retained regression ran once on Windows; there is no duplicate Linux
second-generation claim. The two new parents still require actual Linux
execution and final static/documentation gates.

### Actual final Linux execution

Both new parents ran from a Linux binary compiled from the final frozen source:

```text
TestGatewayRebindConcreteCompositionTransferredLANLifecycle
TestGatewayRebindMultiRunnerCurrentCommandRefusal
```

Both parents and the lifecycle's `missing_proof`/`crossed_proof` subcases passed,
with no failures or skips. Compilation and actual WSL execution each exited 0;
combined wall time was 56.078 seconds and all three source hashes stayed
unchanged. Prefix: `final-wsl-new-parents`. The test ran from the package's
directory with the exact two-parent selection, `-test.count=1` and a 30-minute
limit. It was cross-compiled with Go 1.27.0, Linux/amd64 and CGO disabled; this
is plain Linux execution, not a race-detector or Docker pass.

Binary SHA-256:
`6F374A6516C90F8EA62D75100AAFC7BB7957C3563DDE5ADA67D093E9B292106A`.
Complete execution output SHA-256:
`DA8C197CCE646614AF21CDB9C1A450336151A0E3DEA3BFEEC65544D731FC2EF3`.

### Final static and documentation gates

On the same frozen source, `go vet ./internal/generatedingress` passed with
exit 0 and empty output. `gofmt -d` on the three changed test files also exited
0 with empty output. `git diff --check` exited 0. The static wrapper initially
reported failure because Git emitted LF-to-CRLF warnings; its original result
and 326-byte warning output are preserved. A read-only invocation with
`-c core.autocrlf=false` then exited 0 with no output. No code or test rerun was
needed for this reporting issue. Result prefixes are `final-static` and
`final-source-manifest`; source hashes remained unchanged.

The existing partition patterns select the new lifecycle in
`rebind-composition-native` and the strict command parent in `rebind-history`.
No CI selector, timeout or expected test inventory is weakened. The existing
second-generation regression remains in its own partition. This is source
inspection, not a hosted race result.

Independent security and final integration source review found no blocker in
the real SQL/public lifecycle sequence, retained history/refusal assertions or
the bounded simulator extensions. Final documentation gates before the local
commit are `pnpm --dir docs check:workflow`, `pnpm --dir docs build` and
`pnpm --dir docs check:accessibility`; their actual exits, logs and unchanged
documentation input hashes are recorded in
`temp/m3-transferred-lifecycle-docs-final-20261009/result.json`.

Final code hashes:

| File in `internal/generatedingress/` | SHA-256 |
| --- | --- |
| `gateway_rebind_runtime_composition_test.go` | `7E4BBC8D9BCF6292277BB862B48D49FB3DB511D83CCBE72C81E90293E76E482F` |
| `gateway_rebind_multigeneration_backend_test.go` | `6530BBE5DF0C007AE43B93BB2ACE22973E3CD970A24DF04F58F110633AA6AED4` |
| `gateway_rebind_transferred_lifecycle_test.go` | `20788877AFC7FC2DA6A89869009D5A9B2B402169849E3DFFE8233C64302218DD` |

## Rollback and remaining acceptance

Reverting this test-only unit removes the additional verification without
changing deployed state or production behavior. Retain all SQL and protected
history. Production factory activation, the full crash matrix, hosted Docker
and race gates, and physical-device/interface-drift acceptance remain separate
requirements. This unit does not complete M3.
