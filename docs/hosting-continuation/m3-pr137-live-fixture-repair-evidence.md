# PR 137 live ingress fixture repair

Status: local fixture repair passed focused native and WSL checks. No repaired hosted
Docker outcome is claimed. Published source is
`488046bcfc98000f70ea03322dbfe1084bec6e78` on draft PR 137. Publication of that
revision does not authorize publishing this subsequent repair or merging either
PR 136 or PR 137.

## Preserved hosted failures

The first hosted attempt used actual Linux Docker. These five parent tests
failed; the original job logs are retained under
`C:/Users/huang/Documents/Projects/Rig/temp/`. Parent elapsed times below come
from the saved Go JSON events, not from whole job durations. Outer cleanup steps
reported success. The runtime journeys additionally emitted a strict
terminal-cleanup refusal after failing before rebind admission.

| Parent | Job / log suffix | Result / seconds | Log SHA-256 |
| --- | --- | --- | --- |
| `TestLiveGatewayRebindCrossStorePreparedClaimProcessRecovery` | `113462431921-attempt1.log` | FAIL / 47.22 | `011AAB5060125BC3E69D10396D26F59D4277AA0BF800CB20A82B7AF06E69635A` |
| `TestLiveGatewayRebindRuntimeRollbackAndReplay` | `113462431744-attempt1.log` | FAIL / 50.00 | `695E26149FEFE6775137744E53C723C932236AADBF5E41BF19DCC91341FDF352` |
| `TestLiveGatewayRebindRuntimeCommitAndReplay` | `113462431809-attempt1.log` | FAIL / 55.05 | `3115792E7065E5B4560884559C596036AA1A73F7049C9CF37E4F4A199B9ACC3A` |
| `TestLiveGatewayRebindFinalHandoverCommitAndRestart` | `113462431768-attempt1.log` | FAIL / 112.05 | `B62E5E6AE2A84DB002164F1A46CC68A2D81D4AE24DBBAE99122C423FFF6E40A3` |
| `TestLiveGatewayRebindFinalHandoverRollbackAndRestart` | `113462431898-attempt1.log` | FAIL / 134.27 | `A93131527878ECA25A9C79B12EB57FD27442B787681614FC1D271B95D004D38F` |

Each filename has the prefix `pr137-job-`. These failures remain failed
acceptance attempts, even if deterministic fixture regressions later pass.

The original Docker workflow run `37821215981` completed with 10 successful and
5 failed jobs at the published source. Its final metadata is preserved in
`temp/pr137-docker-run37821215981-attempt1-final.json`, SHA-256
`741ACBF06269F6410817AFDF675B404C72A806BEBD7DAB1890D31C45840EAB6D`. Passing sibling jobs do
not replace the five failed boundaries above.

The separate generated-runtime lifecycle job
`pr137-job-113462430465-attempt1.log` also failed four direct-Manager fixtures:
`TestLiveNextCacheRuntimeRoute` (26.11 seconds),
`TestLiveGeneratedGatewayReadiness` (10.12 seconds),
`TestLiveGeneratedBlueGreenLifecycle` (6.36 seconds), and
`TestLiveHostingNotesDatabaseRoundtrip` (36.18 seconds). That log's SHA-256 is
`8CB60B84159F721DDB6E90CA71F5BF74FD31B51744FDEA9ABBC63A79FABCC524`.

## Confirmed fixture contracts

### Prepared child manifest

`GatewayRebindRosterEntryV2.EntryDigest` is a derived field excluded from JSON.
The parent inspection contains it, but decoding the child manifest loses it.
`prepareGatewayRebindLocked` reconstructs a fresh inspection and compares it
exactly before claiming SQL; the resulting mismatch explains the child's
`route_reconciliation_required` before its intended exit-after-claim hook.
Production's exact comparison is correct and must remain unchanged.

The fixture now clones the decoded roster, rederives only the omitted entry
digests, and verifies canonical ordinal order, operation identity, count, spec
digest and complete roster digest against the unchanged approved spec. Fresh
production reinspection remains authoritative. Missing, mutated or reordered
manifests are rejected. The helper writes no protected history or SQL state. A
two-entry advisory manifest in the regression test is used only to exercise
ordering; it is not installed as authoritative history.

### Loopback-only access

The shared source fixture creates its loopback application's normal empty LAN
head (revision zero, no revision ID) and generated-runtime state. It deliberately
does not grant LAN access. `CurrentAppAccess` correctly returns `ErrNotFound` for
this state. Both typed runtime journeys instead required success before calling
the rebind coordinator, so neither reached its intended commit or rollback.

The fixture now requires a valid, exactly empty
`ReadAppAccessOperatorSnapshot` before and after the journey. Repository errors
and unexpected reservations are rejected. Runtime-head, protected-route,
application-body, wrong-host 404 and withdrawn-listener assertions remain. No
LAN grant is created to satisfy this check.

### Application response bodies

`probeGatewayV2HostStatus` intentionally reads a body only for challenge paths;
its ordinary `/` result has an empty body. The prepared-child and legacy handover
journeys incorrectly compare that result to the application's response text.
Both assertions now use the existing bounded direct HTTP application-body
helper, with exact address/Host, no proxy or redirects, HTTP 200, and exact body
content within 4096 bytes. Wrong-host and withdrawn-listener checks remain.
The deterministic HTTP cases verify exact success, wrong content, non-200
responses and over-limit content. The 302 case has no Location header and
proves non-200 refusal; the explicit no-redirect policy is also source-reviewed.
Production probes are unchanged.

### Direct-Manager repository composition

The four lifecycle fixtures supplied a no-op SQL fence and omitted the current
state repository. Initial routing now reaches
`readOptionalGatewayCurrentSelectionLocked`, which correctly refuses a nil
repository: missing protected files cannot prove the absence of an active SQL
rebind. The normal controller composition already supplies the real repository.

The fixtures now use `appaccess.New` over the real `database.Open` database at
the same `DataRoot` as their ingress manager, and supply both the actual fence
checker and current state reader. The notes journey reuses its existing database;
it does not create a second empty authority store. The other fixtures close their
databases through test cleanup. No no-op reader or production fallback was added.

### Legacy handover child

The two legacy handover children returned `route_reconciliation_required` before
their expected stop/start crash markers. Their private handover path uses its
explicit repository and does not consume the omitted current-state option that
explains the four lifecycle failures. These logs do not localize the refusal.

The test driver now delegates each observation unchanged while recording only
observation/effect counts, last phase and whether the last observation returned
an error. It prints these bounded fields only on the existing failure path. This
can distinguish refusal before any coordinator observation from a failed
observation; it does not identify every internal predicate. There is no added
preflight, proof normalization or ownership bypass. Apart from the independent
body assertion correction, the handover failure remains unresolved and requires
another actual Docker run.

## Verification and remaining acceptance

The authorized local scope is eight Go test files: the implicated ingress live
fixtures, their focused regression file and four direct-Manager lifecycle
fixtures, plus this additive evidence. Production code, SQL
schema, authority comparators, effect factories, CI workflows, immutable history,
and the separate admission-process branch are outside the repair scope.

### Deterministic failing baseline

`temp/pr137-live-fixture-red/` retains the command, outputs, exit codes and
machine-readable `source-pins.json`. The two existing live expressions were
first extracted into test helpers without changing their behavior: manifest
input was returned unchanged and loopback state still required
`CurrentAppAccess` success. This is a regression baseline with behavior-preserving
test extraction, not a pristine checkout run.

```text
go test ./internal/generatedingress -run ^TestLive(CrossStoreAdmissionChildInputJSONRoundTripPreservesPreparedInspection|GatewayRebindRuntimeLoopbackSnapshotUsesZeroHeadOperatorState)$ -count=1
```

Environment: Go 1.26.0, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -buildvcs=false`, and the shared local Go cache. The first
sandbox attempt failed on temporary-directory access (1.201 seconds); it is not
behavioral evidence. Its output SHA-256 is
`7EEE0754C5C6C16A305EB30D18ED7E8FA051836ED82BB6F11F2FEA4B8C5EBC11`.
The authorized elevated rerun failed both intended assertions, with no skipped
test: omitted entry digest after JSON transport, and `ErrNotFound` for the valid
zero LAN head. Package time was 35.218 seconds, exit 1, output SHA-256
`3901F3C2751D2040BCECE0E311145A1C81A4ACF5DADB1F581426573AB00FCF4E`.

The three RED file hashes, captured and checked before repair, are:

| File under `internal/generatedingress/` | RED SHA-256 |
| --- | --- |
| `gateway_rebind_cross_store_live_test.go` | `AEB670C5418A4722D90C0043A9A87E940ABA27E5BC7C144C977CAEF82B30B837` |
| `gateway_rebind_runtime_live_test.go` | `869454BA23CB309D4D79B0FEA3BAA8070EC60979459F2FA8892558561E8D77DB` |
| `gateway_rebind_live_fixture_contract_test.go` | `F922A766E804FC3FA06C1170F3A66A35BE713D2BB6BD8735D20DCAA42918CC09` |

The earlier PowerShell-formatted `source-sha256.txt` omitted hash columns and is
not used as evidence; `source-pins.json` retains the checked values above.

### Real repository and tagged-package checks

`temp/pr137-direct-fixture-checks-20261008/summary.json` and `commands.json`
record exact arguments, unchanged eight-file source pins, published HEAD, exits
and output hashes. All commands exited 0:

```text
go test -json -count=1 -timeout=4m -run ^TestOptionalGatewayCurrentSelection ./internal/generatedingress
go vet -tags=live_docker ./cmd/hostd ./internal/generatedimage ./internal/generatedingress
go test -c -tags=live_docker -o <recorded-hostd-linux-binary> ./cmd/hostd
go test -c -tags=live_docker -o <recorded-generatedimage-linux-binary> ./internal/generatedimage
```

The real-SQL optional-selection gate discovered and passed all five expected
parents and 16 subcases with no skips, package time 19.232 seconds. Its saved output SHA-256 is
`0257C656EE98DBD312D1822BF72E952A7C109F905FF0E7265E73415087BFF889`.
The tagged Linux/amd64 binaries were crosscompiled with CGO disabled, then
actually queried through WSL using exact `-test.list` expressions. Each found
its requested parent exactly once: `TestLiveNextCacheRuntimeRoute` and
`TestLiveHostingNotesDatabaseRoundtrip`. This is compilation/discovery evidence,
not execution of those Docker journeys. The command record uses
`GOFLAGS=-mod=readonly -buildvcs=false -p=1`.

### Focused fixture GREEN

`temp/pr137-live-fixture-green/` retains native JSON output, actual WSL output,
command/environment records, exit records and source pins. The exact anchored
selection is:

```text
^(TestLiveCrossStoreAdmissionChildInputJSONRoundTripPreservesPreparedInspection|TestLiveGatewayRebindRuntimeLoopbackSnapshotUsesZeroHeadOperatorState|TestLiveGatewayRebindRuntimeLANBodyRejectsUnexpectedResponses)$
```

The native command is `go test ./internal/generatedingress -run <selection>
-count=1 -timeout=2m -json`, with the same local toolchain/cache and readonly
module flags as the RED run. A Linux/amd64 test binary was separately compiled,
then executed through WSL with the same exact selection, verbose output,
count 1 and a 2-minute timeout. This is actual Linux execution of deterministic
tests using SQLite and a local HTTP test server, not Docker.

| Run | Exact outcomes | Time | Saved output SHA-256 |
| --- | --- | --- | --- |
| Native Windows | 3 parents + 10 subcases PASS; 0 fail/skip; exit 0 | Package 36.739 seconds | `5FD19926334FCE5CC085CD5B1185127004A65294F287DBE5B9EFAF014FA1D68C` |
| Actual WSL Linux binary | Same 3 parents + 10 subcases PASS; 0 fail/skip; exit 0 | Runner wall time 8.279 seconds | `D6291F71118AAAEF481630F310609D188B3F71FDA97B579B94E458DA0356C1A8` |

The ten subcases comprise five manifest cases and five HTTP cases. The loopback
parent additionally exercises a real reservation refusal within its parent body.
Only the three deterministic parents were selected; no Docker journey was
executed. The native and WSL runs were sequential; at most one other root-owned
Go verification process ran concurrently.

### Reviewed source identity

These are working-file byte hashes for the eight frozen Go files, at unchanged
published HEAD `488046bcfc98000f70ea03322dbfe1084bec6e78`. Native GREEN and the
root-owned direct-fixture checks use these same hashes. The command records
retain absolute paths. `source-after-linux.json` matches all eight entries in
`source-before.json`; no Go source changed during verification. No production
Go file changed.

| Path | SHA-256 |
| --- | --- |
| `cmd/hostd/hosting_next_cache_live_test.go` | `213980444946C8C72F418DD84E7F50AFB02E1A76F785D8721F7C33304EF941A7` |
| `internal/generatedimage/hosting_database_live_test.go` | `FACE108A7FA3A196B719073D36D752368E153D12764E60CAB8DDD50E2FC71A68` |
| `internal/generatedingress/gateway_readiness_live_test.go` | `87A56F6DC88CE402A040972B239962FD0774FE06A1E366029B8E2CD45A122B8F` |
| `internal/generatedingress/gateway_rebind_cross_store_live_test.go` | `2B40372142BD19EE322FAD4AB74BC8EBB38BC8ED5C8B1C7484EB40F2B5059308` |
| `internal/generatedingress/gateway_rebind_final_handover_live_test.go` | `E0A6E9694D1AE2F358B65A8714899F22F9B9FB037194CC1C2812092F531D0473` |
| `internal/generatedingress/gateway_rebind_live_fixture_contract_test.go` | `C26D8F88A06EEC2AF9A151272E5B8EF6B49D906764DDFD09040EB712248D9347` |
| `internal/generatedingress/gateway_rebind_runtime_live_test.go` | `642070F8666046A909B7B207F449AF7A113A406DDF002E59BD59F5408666EB86` |
| `internal/generatedingress/live_integration_test.go` | `FDED2AED75953E0749840F558F9CB037B03B6D247ED7052511C1C3AB16D1EE8E` |

Final read-only `gofmt -d` produced no output; `git diff --check` exited 0 with
only Git's CRLF conversion warnings. All verification processes are terminal.
The final machine-readable records under `temp/pr137-live-fixture-green/` are:

| Record | SHA-256 |
| --- | --- |
| `verification-summary.json` | `372DDC1F1481B3814DE1CD2305A5BB05ECD2A635537ADE33CB593F226C1FEE31` |
| `source-integrity.json` | `C1D9C874C8C4C4B8F53E2FDDCE10ED37C0064BABC5B6DA47EAAFD8920E31229C` |
| `final-static-check.json` | `C2F045D601431AA22654FFA8C510B399B6901642BB649C1610C745A25ADD6E06` |
| `linux-command.json` | `B2E2C7F48AC7694666F96E8356B503C1A9279788E89F4778A1925F5F5997D37C` |

Security source review found no remaining blocker in the scoped patch: derived
transport fields remain bound to approved digests and fresh SQL, zero LAN state
is explicit, repository and protected roots agree, and physical cleanup and
authorization checks are unchanged. This conclusion applies to the local fixture
repair, not to the unresolved hosted handover refusal or overall M3 acceptance.

### Separate documentation correction and unresolved relay failure

The root-owned formatting correction in
`m3-rebind-cross-store-commit-evidence.md` passed
`pnpm --dir docs check:workflow`, `pnpm --dir docs build` and
`pnpm --dir docs check:accessibility`, all exit 0. The retained log is
`temp/pr137-docs-format-verification-20261008.log`, SHA-256
`FC19334E701B3E183571BAD22B842C048BF1BF9D0AD70417B499D245C9208442`.
The build reported its non-failing large-chunk warning. These checks preceded
finalization of this additive evidence document.

Hosted relay job `113462428784` remains failed at
`relay_outage_integration_test.go:79`, the outage `PushSourceEvent` assertion
after initial sync. No relay source was changed, its underlying cause is not
proven, and this fixture patch does not claim to resolve it.

### Remaining hosted acceptance

Actual Docker revalidation must rerun the affected prepared-process, typed
commit/rollback, legacy handover commit/rollback and four direct-Manager lifecycle
parents on a disposable native Linux host. It must demonstrate the applicable
crash markers, exact recovery/replay and terminal outcomes, physical application
responses and clean owned-resource teardown. Local deterministic or WSL tests do
not replace those hosted results. Further publication requires separate
authorization.

## Hosted follow-up at published `1b3eb24` — 2026-10-09

The user authorized publishing the reviewed follow-up. Draft PR 137 was updated
to `1b3eb244c2f29d30b7c8b77faff5bcca995f66d3` after all original workflows
finished and their terminal records were saved. No original run was canceled,
no merge occurred, and the target remained PR 136's branch.

### Confirmed passes

The new generated-runtime Docker lifecycle job `113849127437` passed, including
all four repaired direct-Manager parents and the final cleanup step:

| Parent | Actual PASS elapsed |
| --- | --- |
| `TestLiveNextCacheRuntimeRoute` | 43.91 seconds |
| `TestLiveGeneratedGatewayReadiness` | 35.23 seconds |
| `TestLiveGeneratedBlueGreenLifecycle` | 46.54 seconds |
| `TestLiveHostingNotesDatabaseRoundtrip` | 52.67 seconds |

Their exact Go JSON PASS events are retained in
`temp/pr137-job-113849127437-attempt1.log`, SHA-256
`12D4F76F2B67D31599332E957CF25C70F76630DEC6E0750A048BA186F690B998`.
This is actual hosted Linux Docker evidence, including the application-owned
disposable external TLS services; it is not a deployment of the user's private
application or a managed database feature.

`TestLiveGatewayRebindCrossStorePreparedClaimProcessRecovery` now passed in
46.85 seconds in job `113849127883`. Its saved log SHA-256 is
`7F83DCEDB76DD9B480813D78A04ECCCBCF003E6F003B8753CC891167C80B2F93`.
This establishes the repaired JSON transport and original application-body
assertion in the actual independent-process Docker journey.

Documentation run `37939259651` succeeded: build job `113849127583` passed and
deployment was skipped for the draft. Its terminal metadata is preserved in
`temp/pr137-run37939259651-attempt1-final.json`. No documentation publication is
claimed from that skipped job.

### Retained failures and their narrower boundaries

Gateway Docker workflow `37939259591` completed with **11 passing jobs and
4 failed jobs**. Terminal metadata is
`temp/pr137-run37939259591-attempt1-final.json`, SHA-256
`4BF5A99BFB88AC5C900852868414BBC1B95D1E6B2A532AF9D78A18FA4A526C11`.
The prepared-process pass does not replace these failures:

| Parent / job | Observed boundary | Saved log SHA-256 |
| --- | --- | --- |
| Typed commit/replay / `113849128245` | Reaches typed commit, then `route_reconciliation_required`; strict cleanup cannot obtain gateway admission and retains resources. 68.32 seconds. | `BFD9781AB5DBD6AEB1A1897E1C99493291B040ACB043CD662AEFB3EF41B385B6` |
| Typed rollback/replay / `113849128442` | Does not reach the intended pre-sequence-17 refusal hook; reconciliation fails and terminal-only cleanup retains resources. 105.79 seconds. | `4B189AB2C371893D7232EAD60BAD118DF01D7BF32DB88D833B7A114B4D9D27BF` |
| Private handover commit/restart / `113849128413` | First coordinator observation at `final_config_copied` fails, with zero effects; child does not reach the intended crash marker. 124.11 seconds. | `C9DDEF37BC72D81E529B7F37725C6D766D26EA900D87015AB49BC422C0F771FA` |
| Private handover rollback/restart / `113849127684` | Same first-observation refusal, zero effects, before crash marker. 130.01 seconds. | `AA3D1758EE1FB59B8B012F55E82E6B424FABA9AFCE50BE4FB8DAE6E5435DB62E` |

Log paths have the same `temp/pr137-job-<job>-attempt1.log` pattern. These are
new failed attempts at later boundaries, not successful runtime commit/rollback
acceptance. The new handover diagnostic localizes an observation failure but
does not identify its physical predicate or justify weakening any proof.
The cleanup admission refusal may come from the process fail-stop latch; it is
not evidence that an operating-system lock remained held.

Relay job `113849128418` also failed in 20.09 seconds, now at the durable source
assertion after the deliberately lost ACK and controller reopen. Its context
budget investigation is recorded separately in
[the relay budget evidence](./m3-pr137-relay-budget-evidence.md).
The newly published Linux race and Windows workflows were still running when
this follow-up was recorded. Original race timeout results remain failed;
their optimization and new outcomes require separate evidence. M3 remains open.

### Additional terminal results at the same published source

Observed on 2026-10-09 at approximately 14:24 UTC. The exact source remains
`1b3eb244c2f29d30b7c8b77faff5bcca995f66d3`, attempt one. No workflow was
canceled or restarted. The following final metadata files are retained under
`C:/Users/huang/Documents/Projects/Rig/temp/` as
`pr137-run<run>-attempt1-final.json`:

| Workflow / run | Terminal result | Metadata SHA-256 |
| --- | --- | --- |
| Relay Compose / `37939259624` | SUCCESS, one job | `10C668CAB0F3190091C0BB25BB561002EE40BAF10FEDCF731FC390C4F4504288` |
| Playwright browser / `37939259719` | SUCCESS, one job | `E5040B4603E10CA71FBEE617545E848523F66B2D538D4C7C05A565F31F8A12F0` |
| Migration approval and uncertainty / `37939259632` | SUCCESS, one job | `49394298CEF5806A28304239FA72AFD701D14691A1FEB73CAB8C771A7921F6BC` |
| Hosted controller journey / `37939259579` | SUCCESS, both process restart and authenticated TLS notes jobs | `013CC0D8F0FF13112AAF0838F94C5827CEA803CF83CBD0B42D997501A6076EEB` |
| Two-app LAN / `37939259630` | FAILURE, one job | `048FA803F5D11A214B6C7A8FCB2E47380B4619DA6CB28743CA8F256447CE14EA` |

The LAN parent `TestLiveControllerTwoAppLANJourney` failed after 1015.78
seconds at fixture line 259, while redeploying application A. Its 14-minute
deployment wait expired with durable job status `running`, phase
`apply_runtime` and no error code. Both preceding LAN grants have complete
successful closed traces. The broad job phase is reported before authorization
and image building, so it does not identify a specific Docker command, lock,
or runtime phase. Complete Docker cleanup passed. Its log is
`pr137-job-113849127873-attempt1.log`, SHA-256
`AEFB83CE9D4A8BC9B3BB1004C58DAED118C152BA3B288912C17D9ECBF7BC3810`.
This remains a failed acceptance attempt; no deadline increase or runtime
guard relaxation is justified by this evidence alone.

The Windows core job `113849127799` also failed, although its containing
workflow was still active. The sole reported package failure was
`TestLegacyLocalSourceDraftMigratesAndCompletesManagedComposeDeployment`:
52.65 seconds total, with its 20-second wait for simulated Compose startup
expiring at fixture line 490. The durable job was `running` in `render_compose`,
with zero simulated `up` calls, no error code and no error detail. The log is
`pr137-job-113849127799-attempt1.log`, SHA-256
`3A415B6E47734A9098294CFA5B6A230E34C94C6E4BCE47A736EA3DFF9F27511C`.
The following core steps were skipped after repository tests failed; the earlier
successful Windows workflow at `488046b` does not replace this failed attempt.

One focused local reproduction used the unchanged legacy test and production
Compose executor, Go 1.27.0 windows/amd64, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -buildvcs=false -p=1`, and `CGO_ENABLED=0`:

```text
go test -json -count=1 -timeout=3m -run '^TestLegacyLocalSourceDraftMigratesAndCompletesManagedComposeDeployment$' ./internal/jobs
```

The restricted execution failed earlier at protected configuration setup,
fixture line 441, with `configuration_unavailable`. That result remains in
`temp/pr137-windows-legacy-check-20261009/`; it did not reach the hosted failure
boundary. Normal Windows user execution then passed the exact parent once in
2.07 seconds (package 2.502 seconds, command wall 5.937 seconds), with no skips
or failures. Its result and source hashes are in
`temp/pr137-windows-legacy-check-20261009-normal-user/result.json`; output
SHA-256 is `915FD16E3A38B71F9F72EC7CC9F44849AEF4D7687DA9C95B5F2DE6E18C2CD9EA`.
The test, job service and Compose executor hashes were unchanged before and
after both commands. The local sandbox boundary and hosted wait failure are
distinct. A focused local pass does not establish the cause or repair of the
hosted failure, and no retry-until-pass sequence was performed.

The Windows workflow subsequently completed at the same published source:
**18 successful ingress jobs, one failed core job and one correctly failed
dependent aggregate**. Its immutable final metadata is
`temp/pr137-run37939259666-attempt1-final.json`, SHA-256
`B5045CEB498E8CED5290F8CC7FE697928121CAF01098C8C26EF8BC883B39FBE5`.
The aggregate job `113871683341` log is retained with SHA-256
`B981CF94BFFBA635E7D20F8B65BCF0920C6D835FFF4FD5566E0FF6BA38C43B65`.
The complete Windows result is FAILURE; the 18 ingress successes do not
establish the core checks that were skipped after its test failure.

The generated-runtime lifecycle workflow `37939259638` also finished at the
same source with **14 passing jobs, six failed race groups and a failed
dependent aggregate**. The complete result is FAILURE. Final metadata is
`temp/pr137-run37939259638-attempt1-final.json`, SHA-256
`39332A319F3788D0D19CFF3B2B12AA94B1FB8C6E79DE5DF357408DB91EDDB78A`;
aggregate job `113875249632` log SHA-256 is
`A74EBDFA8125293F1D869BA4AC1B2FA86EED1BE6DC451A2F84086BE395EC715F`.
The last failed config group, job `113849127993`, timed out at `32m0s`;
its log SHA-256 is
`B573989F00D54575BDA434BD7F27EB863B61C896D4CE23B91D235121CC4D5242`.
No data-race marker was found in that log; timeout remains failed acceptance.
The earlier fixed-time race table in
[the capacity evidence](./m3-pr137-race-fixture-capacity-evidence.md)
remains a historical snapshot, not a complete final workflow tally.

### Local failure diagnostics and production compilation

The local follow-up adds failure-only diagnostics to these two fixtures. It
does not claim to repair either unknown hosted stall:

- The LAN fixture selects the exact failed job from at most ten deployment
  records, verifies application/deployment identity, then reads that runtime
  deployment. One shared two-second observational context covers both reads.
  It is detached from the failed journey's cancellation so a terminal snapshot
  can still be attempted, and is canceled when the diagnostic returns. Output
  is limited to fixed read-result labels, allowlisted deployment/runtime/migration
  states and canonical component-state counts. IDs, component names, raw errors,
  configuration and Docker arguments are excluded. Missing, ambiguous and
  crossed identities produce fixed refusal labels. There are no Docker effects
  or changes to the deployment wait, authorization or cleanup.
- The Windows Compose fixture snapshots its existing fake runner requests under
  its mutex. It emits only total/config/up/unknown counts and a fixed last-command
  class. Classification recognizes the controller's existing command shapes;
  arbitrary option values cannot impersonate a subcommand. The existing
  20-second startup wait and success assertions are retained.

The LAN diagnostic helpers and deterministic tests are untagged private test
code, so ordinary controller CI exercises them. Only their live-journey call
site requires the `live_docker` build tag. Independent security review found
no source blocker in the bounded reads, identity selection or sanitized output.
Independent final source review also passed for the aggregate follow-up.
Executable verification is recorded below. These reviews establish local
readiness for a publication request, not hosted acceptance or merge approval.

The repository's production build passed on the local candidate with the
private gateway diagnostic changes applied:

```text
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

This used Go 1.27.0 windows/amd64, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -buildvcs=false -p=1` and `CGO_ENABLED=0`.
The command exited zero in 6.015 seconds with empty output. All tracked
production Go files and `go.mod`/`go.sum` had identical hashes before and after
the build; concurrent test-only edits were excluded from that production
manifest. The record is
`temp/pr137-followup-production-build-20261009/result.json`, SHA-256
`5934E7B8157DDE904BC647977872C6AA1976AC8DD7DE023C3F0AAE2DF1642C42`.
This is local compilation of the recorded working tree, not hosted acceptance
of the unpublished follow-up or a deployed binary.

### Failure diagnostic verification

All three source files stayed unchanged through native checks, Linux compilation
and actual WSL execution:

| File | SHA-256 |
| --- | --- |
| `cmd/hostd/hosting_lan_two_app_live_test.go` | `0D5A8DFEB05023AA113E3BFEF1D29A7C4D6C8E85DA1842EC24BBDB88D32E19DC` |
| `cmd/hostd/hosting_lan_two_app_diagnostic_test.go` | `F13759B1FCBFC54E24165D9534586E548EF9053339A34D79C70C3295C986A372` |
| `internal/jobs/compose_worker_integration_test.go` | `D2CFDF36C43D5E5687C5ED1A532B3A172B1C6E658039036D7322B3C1C81BFF27` |

The exact native selections were:

```text
go test ./cmd/hostd -run '^(TestLanTwoAppDeploymentFailureObservationIsAllowlisted|TestLanTwoAppFailureDiagnosticContextIgnoresParentCancellationAndDeadline|TestLanTwoAppDeploymentFailureObservationRejectsUnreadableOrAmbiguousState)$' -count=1 -json -timeout=2m
go test ./internal/jobs -run '^(TestBlockingComposeRunnerFailureDiagnosticSnapshotIsAllowlisted|TestBlockingComposeRunnerCommandClassRequiresExactComposeShape|TestLegacyLocalSourceDraftMigratesAndCompletesManagedComposeDeployment)$' -count=1 -json -timeout=2m
go vet ./cmd/hostd ./internal/jobs
```

The hostd selection was also executed with `-tags=live_docker`. That command
compiled the live call site and executed the same deterministic diagnostic
parents; it did not select or run the Docker journey. Linux binaries were
crosscompiled with the local Go 1.27.0 toolchain and actually executed in WSL
using the same anchored selections. Tests retained count one and two-minute
package limits. No credentials or live services were needed.

| Execution | Result | Output SHA-256 |
| --- | --- | --- |
| Native hostd | 3 parents and 8 subcases PASS | `2B59E2394E60FBA6734941566EF93023CAB67104D36938E5A6A1B2A3888B31CD` |
| Native jobs | 3 parents and 5 subcases PASS, including unchanged legacy Compose control | `14FB5F33764C3C81365C8D38A743A9E75FA7CF3BA864EF134C08BB36AB8B4D33` |
| Native hostd with Docker tag | Same 3 parents and 8 subcases PASS | `BF35C8CEC9E66F224312B924574C4AB96C01E6178B70BE7EA5EA2F9F4FAE211A` |
| Actual WSL hostd | 3 parents and 8 subcases PASS | `37E6D9D4CC3AEC3B7B6EFBCEA7B63C2ED01C62B15582E6F52771DB880605E54C` |
| Actual WSL jobs from package directory | 3 parents and 5 subcases PASS | `97A06E2F1A59138EBE149302BD9E405A0277B0269B74994FD7CF4DE274415484` |

Scoped vet exited zero; `gofmt -d` and `git diff --check` were clean. Commands,
exit records, exact inventories and source hashes are retained under
`temp/m3-pr137-failure-diagnostics-20261009/`. Its final artifact manifest
SHA-256 is `2151774FCB0DD2D4266BA9A3051AB3D2CDCF057EDFED082C402E179DD41601A2`.

Environment/setup failures remain recorded separately: native Go is unavailable
inside WSL; an initial binary-copy launcher resolved an invalid root target;
and the first executed jobs binary ran from the evidence directory, so its
existing migration fixture could not locate relative migration files. Only the
affected launcher/package-directory execution was corrected using the same
compiled binaries. Native tests and compilation were not repeated to mask these
failures. The final jobs package-directory run passed its existing control in
0.49 seconds. These checks establish diagnostic behavior and test discovery;
they do not establish a repair of the hosted LAN or Windows stall, Docker
acceptance, or Linux race acceptance. Reverting the three test-file changes
restores the prior failure output without changing runtime data or deployment.
