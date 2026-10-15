# M3 controller integration: local acceptance

Date: 2026-10-14. Verified code revision:
`f7a83e49fad9479df4cf8023b09ac9f31bf033ad`.
This is the local acceptance record for the dormant controller Docker candidate.
M3 remains incomplete. Candidate hosted acceptance is pending at publication.

## Integrated scope and history

| Input | Exact revision | Role |
| --- | --- | --- |
| PR #152 | `53d173503eec6b2240108718c17be236032adf9f` | Runtime baseline with native ownership comparison |
| Readiness repair | `1ad62a9f9be7e981c3b9516b682fdea4102dcb03` | Deterministic observer test; unchanged production readiness |
| Controller checkpoint | `1f093ffea78444eca798834c7cf60119f84a4eee` | Dormant admission, authority, recovery and private HTTP Docker harness |

Two ordinary merge commits, `e56141e` then `6f8a12f`, preserve all input
history. The final fix `f7a83e4` binds admission to its closing runtime read.
Source branches were preserved. The aggregate adds migration 036 and its
identical public/embedded copies for resource-scoped recovery audit retention.
The [controller contract](./m3-controller-rebind-contract.md) describes the
lease, live-session, approval, immutable-history and failure boundaries.

The new rebind handlers remain absent from production route registration and
ordinary hostd capability assembly. Only the private test harness registers
them. No deployment or user-controller restart was performed. External
databases remain application-owned; this work adds no database provisioning.

## Closing-read freshness repair

Review found that the final running inventory could contradict an earlier
valid serving proof while admission still accepted that proof. The repair:

- Requires both final live and restart configurations to match the canonical
  approved target, using the existing semantic JSON comparison.
- Derives the full serving attestation from that target and final inventory,
  binding endpoint/network, config, route and listener evidence.
- Preserves existing identity/image checks and the stopped branch. The
  extracted proof function is pure; it adds no runtime effects or reads.

Before the repair, the regression executed two parents and 18 children:
15 invalid children failed and all three valid controls passed. After the
repair, both parents and all 18 children passed, with no skips or unexpected
events. The managed-reader cases change configuration only on the fifth,
closing inventory read and assert exactly five reads and zero effects.
Other cases retain internally valid but incorrect rehashed attestations.
Equivalent JSON formatting remains accepted.

| Evidence | Raw test log SHA-256 |
| --- | --- |
| Expected RED | `bd177fc10937755344884bb7aa70c6ac72d00fa337367216360bc1f1727b64bc` |
| GREEN | `fd5ed9474661c0a2932b0e50d36366e84ac28a76aa4da5e4d5b6f551cd206845` |

Independent manual code and security reviews found no remaining actionable
issues in the repair. This is not a claim of hosted CodeRabbit acceptance.

## Final-source executable checks

Go 1.26.7 was used with `GOTOOLCHAIN=local` and
`GOFLAGS="-mod=readonly -p=1 -buildvcs=false"`. The runner cross-compiled
Linux binaries, executed them through WSL from their package directories,
and used native Windows binaries for the other gates. All Go/SQL/module
inputs, binaries, selections and raw logs were pinned by SHA-256.
Every selected parent and child had the exact expected RUN/PASS count;
missing tests, skips, failures, input drift or capture failures failed the gate.
These focused local runs do not include race instrumentation or real Docker.

Each gate used its literal parent-name alternation, anchored as
`^(name1|name2|...)$`, with `-test.v=test2json`, the table's `-test.count`,
and `-test.timeout=10m`. Complete argv, selections, input manifests and
results are retained under the local evidence directory
`temp/m3-controller-docker-acceptance-20261014/final/`.
Counts describe each gate and may overlap across gates.

| Gate | Parents / children | Repetitions | Execution seconds | Raw test log SHA-256 |
| --- | ---: | ---: | ---: | --- |
| `ingress-commit-wsl` | 28 / 103 | 1 | 88.515 | `c717d4f91a4cd21b3400b9a604c59ac35f129a8215e1976e04ce86433d45b907` |
| `ingress-recovery-wsl` | 17 / 103 | 1 | 160.750 | `ae5890097da2d8110a65638cc8c02b65ce0d9bc811c99017fc1b151abc0c8741` |
| `controller-wsl` | 28 / 165 | 1 | 1.187 | `57b27c98a29b75b8d1890f1f8b2c16e772a7ed3cbe9fe56a5fa214e6fe97d131` |
| `appaccess-native` | 7 / 51 | 1 | 1.938 | `882256637c69c3d345ece04f5b921da35132f77a06327a07501cbbfa1427154c` |
| `native-inventory-wsl` | 8 / 41 | 1 | 63.781 | `055c6532e15b4a0bcea3b5e6c51d84bc9088d724dcd2f641bfcb9d637cb4b1af` |
| `native-inventory-windows` | 3 / 14 | 1 | 208.937 | `03c373a200eb3e13c76ba650093fc02b9a1b1830a839f1e4f908ea0a894be533` |
| `replay-wsl` | 2 / 8 | 1 | 18.953 | `6dffe58f0480681d952c21318c81ff2830de8ef6f1c8311a6656657a33d87deb` |
| `migration-native` | 2 / 0 | 1 | 1.312 | `a1f9508445ec3f616bcb9dfef1990e94c536a09e0a8e515c84a2ad070b18936d` |
| `readiness-repeat-native` | 1 / 0 | 100 | 0.750 | `058ebe221cfbd56e74c9d5b4f6bd7f3f0039051bfb62de19e6bc3ed02de13ab7` |
| `readiness-suite-native` | 8 / 0 | 1 | 0.703 | `5a5e5ac58596e37e2a331be4775bfd3795089525f3fee9f89eaf5be2d0645ee2` |
| `freshness-wsl` | 2 / 18 | 1 | 7.078 | `b92eda66631dcfdcdca6348948893d8a8bc7a7e455633f73fd1bd9c793b47a07` |
| `openapi-wsl` | 1 / 0 | 1 | 0.891 | `3bc00a52ff3e0440472fae5ff73d29f1df5c08471ed0cc85612bf657b2aaba74` |

Additional checks passed on the same frozen code:

```text
git diff --check 53d173503eec6b2240108718c17be236032adf9f HEAD
go vet ./internal/appaccess ./internal/controller ./internal/database ./internal/generatedingress ./internal/relay/store
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
pwsh -NoProfile -File scripts/check-generation.ps1
```

Canonical formatting passed for all 64 changed Go files. The Windows checkout
uses CRLF conversion; LF-normalized working bytes exactly matched both gofmt
output and committed blobs. The earlier raw `gofmt -l` flags were retained as
an environment-formatting false alarm, not treated as successful raw output.
Migration mirrors and generated OpenAPI artifacts matched. The OpenAPI route
contract additionally passed with explicit discovery and RUN/PASS evidence.

## CI selection checks

Fresh compiled inventories contain 1,034 Linux ingress parents, 1,032 Windows
ingress parents and 149 controller parents. Go RE2 evaluation of the actual
captured runner arguments assigned each ingress parent exactly once across
20 groups in both race and plain modes. Controller lifecycle/remainder
selection is complementary; all 51 discovered Linux packages have one owner
after accounting for those two controller partitions.

The three workflow matrices agree. Missing-group and duplicated-selection
fixtures fail; five invalid runner requests are rejected. The committed
synthetic selector contract also passes from exact LF Git blobs, including
list/test/output-capture failure controls. Existing test deadlines and race
flags remain intact. This verifies selection, not execution of the race suite.

## Hosted gate and remaining M3 work

[PR #152](https://github.com/tyhuang9/rig/pull/152), at the exact baseline above,
completed all ten workflows successfully: 99 CI successes and the expected
documentation-deploy skip, plus its external review check. Its typed Docker
forward/replay/rollback and independent cleanup evidence belongs to that
baseline. It does not certify this combined controller candidate.

The candidate must pass its own hosted Docker, Linux race and Windows gates.
The added exact parent is
`TestLiveGatewayRebindControllerRecoveryCommitAndReplay`; the workflow also
requires independent fixture cleanup. The parent prepares P1 and launches
the private authenticated HTTP controller child. Passing it will establish
that specific recovery/replay journey, not supported full-hostd startup.

Remaining M3 acceptance includes supported hostd capability/route assembly,
recovery-only startup, the full controller crash and contention/release-fault
matrix, and physical second-device LAN qualification. No local result here
claims those outcomes. No UI change was made; manual user-facing activation
was not attempted.

## PR #153 CI follow-up: checkpoint fixtures and Node startup

Follow-up code is `04ed6226a4db91f1c0a120d7124b6e183de683b2`, comprising
checkpoint fixture repair `b899640` and the Node watchdog repair `04ed622`.
These commits change five test files only. The production admission proof,
history, recovery coordinator and runtime safeguards are unchanged.

On candidate `76cf574`, Windows job `116091530455` and Linux race job
`116091528830` exposed recovery fixtures that expected an unavailable network
to create a predecessor checkpoint. Admission now correctly requires fresh
physical proof before writing that checkpoint. The repaired tests distinguish
an unproven A0 claim, which must retain identical SQL/protected files and its
fence, from an existing checkpoint that permits a proved no-effect abort.
The drift test now verifies that it reached the no-effect proof; the Docker
absence test requires an operation-scoped inventory read. Emergency jobs
`116091530343`, `116091528837` and `116091530290` exposed the same fixture
assumption in native withdrawal and retained-history controls. Their shared
fixture now interrupts after the actual durable checkpoint.

The old immutable Linux binary reproduced the recovery and emergency/absence
failures. Raw RED logs and their original binary/source provenance remain in
`temp/m3-pr153-hosted-20261014/`. These failures were not fixed by weakening a
production check or constructing a fabricated terminal receipt.

The Windows Node failure occurred at the old five-second parent deadline,
before any JavaScript startup record. The host-side control now grants up to
15 seconds for a validated startup record, then five seconds for completion.
Its JavaScript request timeout remains 1,500 ms; CA verification, peer name,
`rejectUnauthorized: true` and the request itself are unchanged. Both deadline
paths kill and reap the child before reading its output, with a bounded pipe
wait. Subprocess tests exercise no startup and a stall after startup. There
are no retries or additional skip conditions.

Both platforms were freshly compiled with the pinned Go 1.26.7 toolchain and
the same read-only flags documented above. Verification selected:

```text
^(TestRecoverGatewayRebindStartup.*|TestRecoverGatewayRebindPreparedA0RequiresStablePhysicalProof|TestGatewayCurrentNativeEmergency.*|TestGatewayRebindNoEffectRuntimeProvesAbsenceBeforePreparedRollback|TestLiveExternalTLS.*|TestLiveNodeTLS.*|TestRunLiveNodeTLSProbeBoundsStartupAndCompletion)$
^TestGatewayCurrentNativeAbortEmergencyRequiresCompleteBoundHistory$
```

Each binary ran with `-test.count=1 -test.timeout=10m` and test2json capture.
Inputs and binaries remained unchanged throughout every run; every discovered
parent was executed. The Windows runs passed all **28 parents / 36 children**,
including actual Node TLS trust, with no failures or skips. Linux passed
**27 parents / 36 children**; `TestLiveExternalTLSNodeTrust` additionally
reported its existing optional skip because WSL has no Node executable.
The Linux all-selected gate is therefore recorded as **incomplete**, not green.

| Run | Seconds | Raw test log SHA-256 |
| --- | ---: | --- |
| Windows affected suite | 186.906 | `65574814d20fedf7394550618c03e281c845b7296cce3110531198962a40080f` |
| Windows retained abort history | 24.234 | `3f3eb8354ae18732e6e6308fb5729af105415d8253c16af1a0cbdcda51aa99c0` |
| Linux affected suite, Node skipped | 21.515 | `217b118b3f22f6dfbf93882607b93ddacedbf8799699ace49c283253b4fc2175` |
| Linux retained abort history | 4.484 | `9aaaaed531cd84b57149df07540f1b7e926184134a1c4c64f62b1156031d9b65` |

Scoped `go vet ./internal/generatedingress`, formatting and diff checks passed.
Independent manual review of the recovery fixture changes found no issues;
root review caught and corrected an output-before-reap race in the Node
watchdog before these final runs. Full repository, race and actual hosted
Windows cold-start verification remain CI gates for the updated head.

At `76cf574`, existing hosted LAN (one parent), migration (two parents), and
generated-runtime (ten parents / nine children) journeys passed with their
independent cleanup steps. Their evidence is retained with that exact source
and merge tree; it is not a claim that the updated draft has passed CI.
The new private controller recovery/replay Docker parent and full supported
hostd/physical-device acceptance remain separately required.

## PR #153 retained-history fixture follow-up

Code `b65b47afab4dd8e81ba9dab4485a35341141aa1f` adds two test-only corrections
after the first repair batch above. Windows rebind-history job `116091530341`
exposed another retained-rollback fixture using the obsolete network-refusal
prefix, and a malformed-input case that panicked in fixture setup before
production validation. The rollback test now interrupts after its real durable
checkpoint and asserts the no-effect abort call. The inventory fixture parses
the address only when the inventory is requested and returns a parse error;
production can reject the malformed claim first. Existing immutable-history,
generation, current-authority and retry assertions remain in place.

The previous immutable Linux binary reproduced both failures. Fresh Windows
and Linux binaries each passed **four parents / four children**, with no
failures or skips, using the same pinned toolchain and flags as above:

```text
^(TestGatewayRebindCoordinatorCommitsAfterRetainedRollback|TestGatewayRebindLegacyCompositionRetirementCensus|TestGatewayRebindProposalAndAdmissionUseRealRepository|TestGatewayRebindConcreteCompositionLegacySource)$
```

Both runs used `-test.count=1 -test.timeout=10m`, explicit discovery and
test2json capture. All 933 Go/SQL/module input hashes and binary hashes remained
unchanged during execution and match this code commit. Windows completed in
491.547 seconds (log SHA-256
`2c549bf8b0f751111790af039b6bb1dbdfca6b37346bcaf00fd80a30572c1273`);
Linux completed in 88.219 seconds (log SHA-256
`bbb738d6babcce73ff9f44a71dc127531575fa2689012cddf50341b177dc03bd`).
Receipts are `history-green-windows.result.json` and
`history-green-wsl.result.json` in the same evidence directory above.

Independent static review found no issues in these two corrections. CodeRabbit
CLI was unavailable; this is manual review evidence. The earlier 28-parent
Windows / 27-parent Linux results remain attributed to `04ed622`, before these
two fixture edits. They are not rerun results for `b65b47a`. The newer four-parent
suite covers the changed helper and retained-history sequence. Formatting and
diff checks passed. Full candidate Docker, race and Windows CI remain required.

## PR #153 private recovery diagnostic follow-up

At `d013ecf99cbea94b6ab04f7ed3a353432886c56e`, hosted Docker run
[38640034376, job 116113486680](https://github.com/tyhuang9/rig/actions/runs/38640034376/job/116113486680)
failed `TestLiveGatewayRebindControllerRecoveryCommitAndReplay` after
`approved-reconcile`. Independent cleanup passed. The child output was deliberately
suppressed because it handles a fixture session; no retained artifact identified
the exact response or failure. This result does not establish the root cause.

Test-only code `d8fd418fe389f0532371095b9b1abd47b690a194` adds finite outcome
labels for commit, abort and curated HTTP problems; a whitelist parser clamps
unknown output and marks duplicate outcomes ambiguous. Failure reporting also
uses the existing bounded, read-only SQL/protected-state inspection. Raw child
output, response bodies, credentials, operation identifiers and digests remain
suppressed. Existing happy-path assertions, timeouts, immutable binary checks,
cleanup and runtime behavior are unchanged. No retry or acceptance relaxation was
introduced. These diagnostics still require a new hosted run; they are not a
recovery repair or evidence that the Docker journey succeeds.

Go 1.26.7 with the read-only flags above compiled both packages on Windows and
Linux. Explicit test discovery and test2json capture passed five parents / twenty
children per platform, with no failures or skips. All source and binary pins were
stable. Exact selectors and raw-log hashes:

```text
^TestLiveLANGatewayRebindRecoveryApprovedOutcomeIsFinite$
^(TestLiveControllerRecoveryDiagnosticsAreFinite|TestGatewayRebindRuntimeFailureStateDiagnostic.*)$
```

| Receipt / raw log prefix | Parents / children | Test log SHA-256 |
| --- | ---: | --- |
| `recovery-diagnostic-controller-wsl` | 1 / 7 | `5c398f92bc3f7b619957db8be11d9b007a03572502884fdc61e9ec9dbd5e3222` |
| `recovery-diagnostic-ingress-wsl` | 4 / 13 | `587c88b70519efa7e3bc362e1ed7dd49a32ace26beac175dcc05d2b63857f5e3` |
| `recovery-diagnostic-controller-native` | 1 / 7 | `5b632aa2b4f2b31aa7050bab5bc118d83589995104e280b6aee0f91f714f57da` |
| `recovery-diagnostic-ingress-native` | 4 / 13 | `91244b6fa3e1775f440a46f98c36b3202c102547e0f20e7dae17e44f9a70549e` |

Receipts and logs are retained under `temp/m3-pr153-hosted-20261014/`.
Independent bounded review found no additional credential, authority or parser
issue. Formatting and diff checks passed. Full hosted Docker, race and Windows
acceptance remain required.

## PR #153 supported constructor repair

Code `20f0d37d43d6c29f84722af126476f9935b9e5a0` repairs the concrete cause
identified after the diagnostic run above. `New` previously returned a Manager
without the coherent physical adapter pair. Preterminal rebind attestation had a
managed fallback, while committed-current predecessor retirement required an
interface unavailable on the ordinary nil-driver fallback. Recovery could therefore
record the immutable commit, then fail the mandatory retirement check and return
HTTP 503. The observed commit receipt and successor authority must not be rolled
back to conceal that error.

`New` now installs the existing rebind/current adapters on the same Manager,
runner, protected stores, locks and fail-stop identity. This is the same reviewed
constructor wiring already present in the supported-controller candidate. It
performs no Docker action during construction and does not bypass any invocation,
retirement, ownership, fence, session, proof or release guard. Raw `newManager`
remains an explicitly incomplete test seam. Production rebind routes remain
unregistered on this prerequisite branch; full supported hostd acceptance is
tracked separately. Factory comments and the controller contract now distinguish
candidate wiring from acceptance and release.

The exact constructor regression failed on the prior source with both drivers
nil (raw RED log SHA-256
`56763fafdc146e199cd8efafecd7c99e53fe21a7d6cc3eb23793c15fc5b57f12`).
Fresh focused checks then passed without skips or failures, using the same Go
1.26.7 discovery, read-only flags, ten-minute timeout and immutable input/log
accounting described above. Linux additionally covers concrete commit/replay,
committed-history attestation and private-directory refusal; both platforms cover
retirement chains, guard rechecks and exact shared constructor identity.

| Receipt / raw log prefix | Parents / children | Test log SHA-256 |
| --- | ---: | --- |
| `constructor-green-wsl` | 8 / 7 | `c2c9574c4d526aced90d783185480d6066cb9afd834d98a364657d48ea38f605` |
| `constructor-green-windows` | 5 / 5 | `1677701725dba8aa4e450c98dc76371541571415eb62c61a8bd086f751b27d24` |

Receipts remain under `temp/m3-pr153-hosted-20261014/`. The only source delta
after these green runs is the factory's explanatory comment; executable code is
unchanged. Fresh CLI builds and scoped vet cover the final comment-normalized
source. Independent bounded code/security review found no guard or scope regression.
These focused tests do not by themselves establish hosted Docker or M3 acceptance.

### Hosted constructor repair verification

At exact published source `386bad5f62686e1f5a7bd549ef367ec0fc26dad8`,
[run 38646046781, job 116133035604](https://github.com/tyhuang9/rig/actions/runs/38646046781/job/116133035604)
passed `TestLiveGatewayRebindControllerRecoveryCommitAndReplay` on 2026-10-14.
Checkout used GitHub's test merge `683ce0217b847734418017be82ace76a34caee45`
with parents `53d173503eec6b2240108718c17be236032adf9f` and `386bad5`.
Its tree `e06eaf2f215173354999c45c85d86456cb7eab09` equals the published head's
tree, so the tested source content is exact.
The captured test stream contains one RUN and one PASS, no failure or skip,
and elapsed test time 181.1 seconds. The separate
`Require complete cross-store fixture cleanup` step also passed.

The raw job log SHA-256 is
`3630601ebfd63fb1f2455f2b640c961c28435e8f7508b6a135abd038f2c2e99e`.
The job metadata, raw log and executable event/cleanup receipt are retained as
`386bad5-controller-recovery.*` under `temp/m3-pr153-hosted-20261014/`.
This verifies the repaired private authenticated HTTP recovery journey. It does
not establish the full supported-hostd crash matrix, all platform/race gates or
physical second-device LAN acceptance; those remain separate requirements.

## Disablement and data integrity

Keep new production rebind routes unavailable while acceptance is incomplete.
The candidate Manager now installs its coherent guarded physical adapter pair;
that wiring does not itself establish supported hostd acceptance. To stop candidate testing, stop its disposable harness and
require independent fixture cleanup. Do not delete migration/audit rows,
protected decision history, retained recovery evidence or application data
to simulate rollback. Any later production activation requires its own
reviewed recovery and rollback plan; this draft does not authorize it.
