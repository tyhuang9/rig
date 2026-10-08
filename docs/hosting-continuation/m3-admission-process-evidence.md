# M3 pre-effect admission across processes

Status: bounded implementation and verification complete on 2026-10-07. Four
new parent tests pass on Windows and Linux/WSL; existing targeted regressions,
static checks, and independent QA pass. M3 remains incomplete. No publication,
integration into the runtime branch, GitHub merge, or deployment is authorized
by this record.

Branch: `feature/hosting-m3-admission-process-gate`.
Base: `603c38f47be35d2c47c51d24ca4a1fa88a0a228c`.
The separate runtime draft candidate has since advanced to `9470a79` with the
same test-fixture correction described below. It does not include this unit.

## Purpose and boundaries

Prove pre-effect admission using separate operating-system processes sharing
real SQLite, protected gateway files, and the actual deployment-effects and
gateway locks. This addresses the cross-process admission requirement in the
[cutover contract](./m3-rebind-cutover-contract.md).

Only external predecessor observations are simulated. No result from this unit
can establish live Docker behavior, complete hostd restart, the full crash matrix,
terminal recovery, or second-device LAN access. Production factories stay closed.
No public rebind API/UI, managed database, or Neon provisioning is added.

## Verification plan

1. Introduce nonterminal work through a second SQLite connection/process at the
   actual claim-writer boundary. Require refusal from the real writer after its
   transaction observes that work, with no claim, successor artifact, or effect.
2. Use real child processes to attempt worker assignment, startup recovery, and
   ordinary gateway work while admission holds the actual locks. Prove attempted
   entry, bounded exclusion, and no assignment/executor/mutation movement; require
   successful entry after normal release so a broken fixture cannot pass.
3. Terminate an owned admission child immediately after its prepared SQL claim
   commits and before protected installation. A distinct child must reopen the
   same stores, reacquire locks, and reconstruct the exact admitted checkpoint,
   intent, and first progress record. A further replay must leave SQL and
   protected bytes unchanged, preserving the fence and predecessor authority.

Use explicit bounded readiness/continue signals, verified child exit reasons,
and complete reaping. A returned injected error or a fresh Manager within the
same process is insufficient evidence of abrupt process termination. Test
manifests must not manufacture successor state or serialize credentials.

Run every exact parent test and relevant existing admission/OS-lock regressions,
affected-package vet, full Go build, formatting and diff checks. Check that CI
selects every new parent test. Record actual outcomes and limitations below
before treating the unit as verified.

## Baseline and fixture correction

The clean `603c38f` baseline discovered exactly four selected tests. Three tests
and two subcases passed, but `TestGatewayRebindProposalAndAdmissionUseRealRepository`
failed: its direct proposal omitted `RuntimeHeads`, so structural validation
returned `ErrInvalidInput` before the expected nonterminal-work refusal.
Production preparation already supplies `inspection.RuntimeHeads`.

The fixture now validates the inspected census count/digest and supplies it to
the writer. Exact busy-refusal, successful admission, replay, and no-effect
assertions remain unchanged. The corrected fixture has identical bytes in the
runtime and process worktrees. On the corrected runtime worktree, all four
tests and two subcases passed, exit 0, 6.786s. This establishes the shared fixture
baseline; it does not execute the new process tests.

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=5m -json ./internal/generatedingress -run '^(TestGatewayRebindProposalAndAdmissionUseRealRepository|TestGatewayRebindPreparedAdmissionAndRecoveryUseRealRepository|TestGatewayRebindEffectBoundaryHoldsDeploymentEffectsLeaseAgainstAnotherProcess|TestGatewayOSLockReleasedWhenProcessExits)$'
```

Logs retained under `C:/Users/huang/Documents/Projects/Rig/temp/`:

| Prefix | Outcome | JSON SHA-256 |
| --- | --- | --- |
| `m3-admission-process-baseline-20261007` | Failed unchanged baseline, exit 1, 6.787s | `22C2B0CC2F6E5705FA93D3CA543B21F711F6F7D52BB7F8836C1AE1C62BA6FACC` |
| `m3-admission-census-fixture-corrected-20261007` | Corrected fixture: 4 tests/2 subcases passed, exit 0, 6.786s | `BDD22FA833005FEDDEC29BE48DACAFB75BC66EC660C202A7F56CBEC2AFC06239` |

No production code or history changed to make this baseline pass. The original
failure remains recorded. Implementation and new acceptance evidence follow.

## Implemented coverage

- `TestGatewayRebindAdmissionProcessRejectsBoundaryWork` reaches the actual
  prepared-admission writer boundary. A child inserts queued/preparing work in
  a real `BEGIN IMMEDIATE` transaction. After delegation to the actual writer,
  the claim remains incomplete during a bounded observation window while the
  child holds its transaction. After the child's verified commit, that same
  claim returns the exact nonquiescent refusal. No claim, protected successor,
  or external command is admitted. The window checks absence of early completion;
  it does not prove when the operating system scheduled SQLite's lock attempt.
- `TestGatewayRebindAdmissionProcessSerializesContenders` parks the real
  preclaim path while holding effects, Manager, and gateway OS locks. A distinct
  `Switch` process times out without state movement. The preclaim attempt is
  canceled before inserting SQL; a fresh process then succeeds with the
  identical no-op `Switch`. The fixture never clears or bypasses a prepared
  fence to make that positive control pass.
- `TestGatewayRebindAdmissionProcessRecoversCommittedClaim` kills and reaps the
  exact admission child after committed prepared SQL, before protected writes.
  The parent process's ordinary `Switch` is still denied by the retained SQL
  fence after OS locks are released. Distinct recovery and replay children reconstruct the
  exact admitted checkpoint/intent/first progress, preserve all predecessor
  fingerprints, and leave SQL and subsequent protected bytes unchanged.
- `TestGatewayRebindAdmissionProcessSerializesHostdContenders` uses actual
  `deploymentEffectsAdmission`, `prepareRuntimeWorker`, and worker admission.
  A held lease prevents recovery or assignment; normal release allows exactly
  one `daemon_restarted` event, one assignment, and one worker execution.
  The hostd holder is the shared admission lease, not a full rebind claim.

Every child has registered bounded cleanup, explicit readiness/control signals,
checked completion, and process reaping. Successful paths check release errors.
Child command attempts are counted and required to stay zero. Topology and
predecessor observations remain pinned simulations; no Docker operation supplies
acceptance here. The positive gateway control is explicitly a no-op request.

## Final executable results

Go 1.26.0. Windows execution used the existing toolchain and module lockfile.
All listed parent tests were individually discovered, their pass events checked,
and package completions and saved exits verified. No selected test skipped.

```text
go test -json -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^TestGatewayRebindAdmissionProcess(RejectsBoundaryWork|SerializesContenders|RecoversCommittedClaim)$' -count=1
go test -json -mod=readonly -buildvcs=false -p=1 ./cmd/hostd -run '^TestGatewayRebindAdmissionProcessSerializesHostdContenders$' -count=1
go test -json -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^(TestGatewayRebindPreparedAdmissionAndRecoveryUseRealRepository|TestGatewayOSLockReleasedWhenProcessExits|TestGatewayRebindEffectBoundaryHoldsDeploymentEffectsLeaseAgainstAnotherProcess)$' -count=1
```

The first two commands pass four new parent tests. The last passes three
existing parent tests and two subcases. The separate corrected baseline above
also passes the real direct-proposal test; overlapping runs are not added
together as distinct coverage.

Linux execution used `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` test binaries
cross-compiled by the installed Windows Go toolchain, then executed under
Linux `6.18.33.2-microsoft-standard-WSL2`, x86_64. This is actual Linux process
execution, not compilation-only evidence. No Linux compiler was installed.
It does not enable race instrumentation.

```text
go test -mod=readonly -buildvcs=false -p=1 -c -o <generated-linux.test> ./internal/generatedingress
go test -mod=readonly -buildvcs=false -p=1 -c -o <hostd-linux.test> ./cmd/hostd
go tool test2json -t -p github.com/hostd/hostd/internal/generatedingress wsl.exe --cd <package-directory> --exec <generated-linux.test> -test.v=test2json '-test.run=^TestGatewayRebindAdmissionProcess(RejectsBoundaryWork|SerializesContenders|RecoversCommittedClaim)$' -test.count=1 -test.timeout=5m
go tool test2json -t -p github.com/hostd/hostd/cmd/hostd wsl.exe --cd <package-directory> --exec <hostd-linux.test> -test.v=test2json '-test.run=^TestGatewayRebindAdmissionProcessSerializesHostdContenders$' -test.count=1 -test.timeout=5m
```

Exact binary paths and argument arrays are retained in
`temp/m3-admission-process-linux-compile-20261007.json` and each run's
`.command.json`. The launcher `temp/m3-run-process-linux-20261007.py` passes
arguments as an array to avoid shell reinterpretation. Both first launch attempts
failed before test discovery because PowerShell/WSL split or interpreted test
flags; their logs remain at the prefixes without `-final`. They are not test
failures or passing acceptance. Direct `--exec` with exact arguments corrected
the launcher; no source change was made for that rerun.

All prefixes below are under the same `Rig/temp/` directory and have companion
`.jsonl`, `.command.json`, `.exit`, and `.summary.json` files:

| Prefix (suffix `-20261007`) | Actual result, exit 0 | JSON SHA-256 |
| --- | --- | --- |
| `m3-admission-process-generated-final` | Windows: 3 tests, 5.115s | `BB7DA0A3010436906B5CD0CDF9892634F8C39A6D9F55393C3BF456677412A70F` |
| `m3-admission-process-hostd-final` | Windows: 1 test, 2.560s | `85401ABB240969B5ADDD2E5EF0A82036778925F694FB6C8DD4125537493728B2` |
| `m3-admission-process-existing` | Windows: 3 tests/2 subcases, 5.510s | `500B53644E8995D9AFCA1F517CE2E981BCE05924BFB15C79F8CD709DDE8A0B96` |
| `m3-admission-process-generated-linux-final` | Linux: 3 tests, test2json process elapsed 4.452s | `8465AA2E6A15702BF261F789D873E7F4545E76DE922D5B29C91D2E8AD8A37AB4` |
| `m3-admission-process-hostd-linux-final` | Linux: 1 test, test2json process elapsed 4.485s | `C89D3AC4A83DEAEAFA6F473455A85A1B4BABFA68939E2F2487E9F570F26B5AC9` |

Source pins for the initial executions above are identical for both platforms.
The strict-kill correction and replacement crash evidence follow below:

| New test file | SHA-256 |
| --- | --- |
| `internal/generatedingress/gateway_rebind_admission_process_test.go` | `A3A1D59CE135D89F22BE96325588D80D83AC76EFEAADD29C752971FE82570EA0` |
| `cmd/hostd/gateway_rebind_admission_process_test.go` | `52741688F6E1FD77F73D0050DCDEDFE8A9048FB0C3379959DA5D0567A71790A5` |

Additional checks passed before the strict-kill assertion correction:

```text
go vet -mod=readonly -buildvcs=false -p=1 ./cmd/hostd ./internal/generatedingress
go build -mod=readonly -buildvcs=false -p=1 ./...
gofmt -l <the two new test files and corrected repository fixture>
git diff --check
```

Vet/build command arrays, exit 0, and empty output are retained in
`temp/m3-admission-process-static-20261007.json`. Formatting reports no paths.
Go's regex engine checked the actual shared script's 13 ingress filters: each
of the three new ingress parent names selects exactly once in `rebind-history`.
The hostd parent remains in the unfiltered core/repository package gates. CI
definitions are unchanged. The local selection checker first rejected a shell
variable containing a digit; its corrected parser passed. No failed checker
attempt is counted as test acceptance.

## Review, limits, and rollback

Independent QA reviewed the initial source hashes and actual entrypoints and
returned GO for this bounded unit. Final integration review then found that the
intentional-kill helper accepted an already-exited child or an ordinary helper
failure. The earlier crash passes are insufficient proof of an abrupt kill.
The helper now requires a successful kill call and a reaped OS kill status:
Windows exit 1 from `TerminateProcess`, or non-Windows signal 9 (`SIGKILL`). It
rejects the helper's normal failure exit 2 and any `os.ErrProcessDone` result.
Review confirmed this closes the identified proof gap.

Only `TestGatewayRebindAdmissionProcessRecoversCommittedClaim` uses the changed
helper. It was rerun on both platforms against generated-ingress file SHA-256
`A3F85980E8ADBE2D6B4985BA0EA6CD94FC227F4440A50D96889DCEFDF04D5B10`;
before/after source pins matched. The hostd source pin is unchanged. Each rerun
discovered exactly that parent, passed without skips, and returned exit 0:

| Prefix (suffix `-20261007`) | Strict-kill result | JSON SHA-256 |
| --- | --- | --- |
| `m3-admission-process-crash-strict-windows` | 1 test, 1.957s | `0AD807B4AD0959AFAD2AFB16845D799031CD3BE2980F15BBE7FE2953F85C8D87` |
| `m3-admission-process-crash-strict-linux` | 1 test, test2json process elapsed 4.132s | `CCB8AB203BCAA6A372103BE4713B8E15193B7C6B068730BBF9648F0FA3BB2C04` |

```text
go test -json -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress -run '^TestGatewayRebindAdmissionProcessRecoversCommittedClaim$' -count=1 -timeout=5m
```

Linux used the same cross-compilation and direct WSL execution method described
above, with a new binary and that single exact test selection. The launcher
`temp/m3-run-process-crash-20261007.py`, the `*-strict-linux-compile-20261007.json`
record, and each prefix's command/source/exit/summary files retain exact inputs
and outcomes. Formatting and the final staged diff check also pass. The broader
test selections and vet/build were not repeated for this assertion-only change;
both platforms compiled it during the focused runs.

During implementation, review corrected pipe `Sync` (Linux returns `EINVAL`),
child cleanup/reaping, ignored release errors, and child command-attempt proof.
A missing manifest `EntryDigest` was handled by the canonical existing digest
function after JSON decoding; it does not manufacture durable successor state.

This is partial coverage of the admission/crash contract. The hostd case tests
real job recovery but seeds no nonterminal deployment. It does not establish a
full running controller restart, terminal rebind recovery, every durable-write
or Docker-effect crash boundary, power-loss durability, Linux race behavior,
physical Docker networking, or second-device LAN access. The full repository
and frontend suites were not repeated for this test-only unit.

No production code, schema, protected format, or factory default changes.
Rollback removes these test/evidence additions while retaining all actual SQL
and protected history. Publication and any local integration into the runtime
branch still require separate authorization; the runtime draft request covers
only `9470a79` and excludes this unit.
