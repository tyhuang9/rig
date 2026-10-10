# PR 137 bounded handover diagnostics

Recorded 2026-10-09. Published source:
`1b3eb244c2f29d30b7c8b77faff5bcca995f66d3`.
Local implementation and bounded verification are complete; independent final
source review passed. No new publication or actual Docker outcome is
claimed by this record.

## Preserved hosted failures

Docker workflow `37939259591` completed with 11 successful and four failed jobs.
Its terminal metadata is saved in
`temp/pr137-run37939259591-attempt1-final.json`. Individual logs are under
`C:/Users/huang/Documents/Projects/Rig/temp/`.

| Job | Observed failure | Log SHA-256 |
| --- | --- | --- |
| `113849127684` | Private handover rollback child: zero effects, one coordinator observation, last phase `final_config_copied`, first observation failed; child 0.81 seconds | `AA3D1758EE1FB59B8B012F55E82E6B424FABA9AFCE50BE4FB8DAE6E5435DB62E` |
| `113849128413` | Private handover commit child: same zero-effect, first-observation refusal; child 0.76 seconds | `C9DDEF37BC72D81E529B7F37725C6D766D26EA900D87015AB49BC422C0F771FA` |
| `113849128245` | Typed commit reached the coordinator, then returned `route_reconciliation_required`; 68.32 seconds. In-test cleanup could not acquire admission and outer cleanup retained a Created stage container, network and volumes | `BFD9781AB5DBD6AEB1A1897E1C99493291B040ACB043CD662AEFB3EF41B385B6` |
| `113849128442` | Typed rollback refused before the intended pre-sequence-17 injection (`refused=false`); 105.79 seconds. Retained inventory included a Created final container and no accepted terminal cleanup authority | `4B189AB2C371893D7232EAD60BAD118DF01D7BF32DB88D833B7A114B4D9D27BF` |

The private children had already reconstructed validated protected state before
the first physical observation. Their exact failing physical predicate remains
unknown. The typed journeys reached different later physical states; these logs
do not establish one shared root cause. The commit cleanup failure is not proof
that an operating-system lock remained held: ordinary admission can also be
refused by the process fail-stop latch. Cleanup authority is not bypassed.

The prepared-claim process job `113849127883` passed in 46.85 seconds at this
same source. Its log SHA-256 is
`7F83DCEDB76DD9B480813D78A04ECCCBCF003E6F003B8753CC891167C80B2F93`.
That separate result does not make the failed handover/runtime jobs pass.

## Approved diagnostic scope

The local follow-up adds private fixed-stage information at existing physical
observation refusal sites. Stage labels come from a closed allowlist. A wrapper
retains a freshly constructed generic boundary error; public `Error()` text,
diagnostic code and `errors.As` handling remain unchanged. It never retains an
upstream error's message, type, arguments or result data.

The second observation currently sanitizes upstream errors. That behavior must
remain: only a recognized private stage may be copied onto a new generic error.
Unknown errors stay generic. Stable-read mismatch retains the same predicates
and may carry only its fixed stage. Observation ordering, reads, validation,
effects, authority, cleanup and default factory gates remain unchanged.

Existing live failure logging may print the recognized stage. The typed live
fixture may record only successfully appended and validated progress and an
allowlisted command class from its existing delegating runner. It must not log
raw Docker arguments, output, IDs, configs, paths or credentials, or re-probe
state to produce a diagnostic. Replay mutation auditing remains enforced.

The caller source audit found no direct error identity, equality, string, type
switch or sentinel dependency in this path. Mutation callers already collapse
observation failures back to the generic boundary. Independent security review
found no blocker in the actual diagnostic diff. Independent final source review
also passed; actual hosted Docker acceptance remains outstanding.

## Verification and remaining acceptance

No diagnostic source edits occurred during the separate database-template
benchmark or its source-pinned checks. The following records are under
`temp/m3-handover-diagnostics/`.

The first-read baseline failed before production diagnostic edits: the real
driver returned its expected generic error but had no `input` stage. This was
one selected parent, exit 1, package 1.190 seconds. Its received tool output is
preserved as a transcript, not a redirected JSON capture, in
`red-first-read-stage.txt`, SHA-256
`2EC808235DAB91955EAA744B6177486050D084B91603FCD16621D047A90D9406`.
The existing production body was from the published revision; the independent
test-template change and root relay change were already in the working tree.
The baseline is not represented as a globally clean checkout.

The initial native command had an incorrect complete-name anchor for the
diagnostic prefix. It passed four other parents and 21 subcases in 1.568 seconds
but did not select the four diagnostic parents. That partial result remains in
`green-go-test.json`, SHA-256
`6542B2232A9C8F67C3180984E367240BDB856A45957E82D5FDC86C6FD411CD28`.
It is not the diagnostic acceptance run. A corrective four-parent selection
passed in 1.174 seconds. The final six-parent selection then included the
canceled-context assertion and both metadata tests.

Six-parent native command:

```text
go test -json ./internal/generatedingress -run '^(TestGatewayRebindHandoverDiagnostic.*|TestLiveGatewayRebindRuntimeDiagnosticRunnerDelegatesAndPreservesError|TestLiveGatewayRebindRuntimeValidatedProgressUpdatesOnlyAfterAppend)$' -count=1
```

| Check | Result | Output SHA-256 |
| --- | --- | --- |
| Native Windows | Exact six parents PASS, zero failures/skips; package 1.242 seconds; exit 0 | `37C88DE9D6023D0863905BF740E20F21A1AF9F1979863E0B7EE652C08E21EAD4` |
| Actual WSL Linux binary | Same exact six parents PASS, zero failures/skips; exit 0 | `47265406A7FF8F0CCE45A6952C14FAA2A51264247CED2B04903464D2708A81E4` |

Final review then made callback counts and the cancelled diagnostic code/text
explicit assertions in the four diagnostic tests. Production and both metadata
tests remained unchanged. The final assertion-only source was checked with
`go test -json ./internal/generatedingress -run '^TestGatewayRebindHandoverDiagnostic' -count=1`
and the same four names in a newly cross-compiled WSL binary:

| Final assertion check | Result | Output SHA-256 |
| --- | --- | --- |
| Native Windows | Exact four diagnostic parents PASS; zero failures/skips; package 1.145 seconds; exit 0 | `A7FC1D0F8C11C4253A711C1CD115549C1CAEB75B64FC0961A72453EF12EB25E2` |
| Actual WSL Linux binary | Same four parents PASS; zero failures/skips; exit 0 | `DC4FAFDCD7B72D50CE0BE7C18E10EFD44F366FA0DFB9B01BBA9F57F06CB5E4A5` |

These replacement results are in `final-contract-*` records. The unchanged two
metadata parents retain their six-parent-run evidence above. This is six
distinct new parents with passing outcomes, not ten distinct tests.

The first test calls the real driver. The second-read and stable-read tests call
the same private stateless two-read body to which the driver directly delegates.
They establish first/second read order and count, unequal-read refusal,
canceled-context refusal and fresh generic sanitation. They do not construct a
complete physical Docker proof. No mutable driver hook or global test callback
was added. Metadata tests establish runner delegation/error preservation and
progress update only after the existing validating appender succeeds.

An initial attempt to invoke Go within WSL failed with `go: command not found`.
That terminal environment failure remains in `final-wsl-go-test.txt`. No tool
was installed. The established route was then used: Windows Go 1.27.0 cross
compiled with `GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local` and
`GOFLAGS=-mod=readonly -buildvcs=false`, then WSL executed the binary with the
six exact names, `-test.v -test.count=1 -test.timeout=2m`. Native commands
explicitly set the shared `GOCACHE`; their command records retain the actual
arguments rather than implying the cross-compile flags applied to every run.
No race-instrumented execution is claimed. Scoped package vet exited zero before
the final assertion-only addition; no production edits followed it. Final
gofmt reported no changes and `git diff --check` passed.

All commands are terminal. Final before/after native and WSL source manifests
agree, SHA-256
`1F78AE74D275E3388B10E588F02919286ADB13E23CDB80A2AA35F6A114EDC315`.
The five owned source SHA-256 values are:

| File under `internal/generatedingress/` | SHA-256 |
| --- | --- |
| `gateway_rebind_final_handover_inspect.go` | `9E05F1C4E5AD06B838C093E78A23C50F9706D127F06216EF7549187B9C677B8E` |
| `gateway_rebind_final_handover_driver.go` | `4C74A3712016872BF37C630A7A9E3213B328E09F73900C59DB3650288C9BFE82` |
| `gateway_rebind_final_handover_live_test.go` | `C04D1288B57426251F191C9363D4EE29DCD14AE3D93900D87856FBF1F4D3704C` |
| `gateway_rebind_runtime_live_test.go` | `367F8DCB073FC8150B7E0911468AF0F1D8BCB1DDFD06EAB880D5DB919B0D6CA8` |
| `gateway_rebind_handover_diagnostic_test.go` | `27ABD15042C8E5625DE6391EB2C4DC99433CA9D3F6DB3A3BE1790F2280509A4B` |

A read-only Go-regexp check used the unchanged runner's previously captured
18 ingress selections and all nine new ingress parent declarations. Each is
selected exactly once: the three template and four handover diagnostic parents
by `rebind-history`, and both `TestLiveGatewayRebindRuntime...` metadata parents
by `ingress-remainder`. Those two are ordinary unit tests with no opt-in or skip.
The result is `temp/m3-pr137-new-parent-coverage-20261009.json`, SHA-256
`6849527D4FF0D7456D6F3251FE0B00E770D8DFFCFBA69FB31F56A5D6268A2F9D`.
This check is new-name classification, not a new full compiled test inventory or
complete suite execution.

This is a diagnostic change, not a repair of the unknown physical predicate.
Actual Docker revalidation remains required after a separately approved update.
The local Linux-engine endpoint is unavailable. Neither local plain tests nor
successful earlier hosted fixture jobs establish Linux race, physical LAN or
the remaining handover/runtime acceptance. M3 remains incomplete.
