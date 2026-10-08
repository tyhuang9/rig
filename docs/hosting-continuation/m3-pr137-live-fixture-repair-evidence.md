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
