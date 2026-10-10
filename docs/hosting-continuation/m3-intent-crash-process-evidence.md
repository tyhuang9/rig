# M3 protected-intent process crash boundary

Recorded 2026-10-09. Local branch `feature/hosting-m3-intent-crash-gate` starts
from `34ca3f9820a96f75b08ce069390f8603bf324415`, preserving the separate
admission-process branch and all its commits. This unit is independent of the
reviewed runtime follow-up at `f49cf793bfae06f3d28fa45d4609a9489075cccb`.
No merge, publication, controller restart or deployment is part of this work.

Status: the bounded crash/recovery selection passed on Windows and actual Linux
under WSL. Hosted Docker, Linux race and physical LAN acceptance remain separate
requirements; this result does not complete M3.

## Purpose and boundary

The preceding process gate covers a retained prepared SQL claim, a protected
checkpoint and completed progress one. It explicitly leaves a gap after the
protected intent is installed but before progress one is written. This unit
extends that matrix with an actual child-process death at that exact boundary.

The design follows the existing private per-Manager after-claim test seam. A
context-only, nil-default hook runs after successful durable intent installation
and readback, before any progress-store operation. There is no public option,
production environment trigger, global hook or mutable authority payload.
The normal nil path retains existing locking and ordering. Hook errors must be
sanitized through the existing proposal-error boundary while preserving the
prepared claim, active fence, checkpoint and intent. Completed preparation
replay must bypass the hook.

The process test establishes all of the following:

1. The child validates actual prepared SQL, the approved roster/runtime heads,
   exactly one checkpoint and one intent, no progress or terminal artifact,
   and zero runner commands before announcing its pause.
2. The parent independently proves the two added protected files, then kills
   and reaps that child using the existing strict process handshake.
3. A separate recovery process adds only progress one and retains the prior
   protected bytes and SQL authority. Another fresh recovery is byte-stable.
4. The SQL fence stays active and no Docker command or serving activation occurs.

This is process-death and retained-state recovery evidence. It does not prove
power-loss durability, real Docker networking, a complete hostd restart, all
later crash boundaries, Linux race behavior or physical second-device LAN.
Production rebind factories remain closed. External databases remain
application-owned; no managed database or Neon provisioning is introduced.

## Clean baseline

Before any Go edit, the source was clean at the exact branch base above.
The following anchored selection ran once with the actual local Go 1.27.0
windows/amd64 toolchain, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -buildvcs=false` and the task's shared Go cache:

```text
go test ./internal/generatedingress -run '^(TestGatewayRebindAdmissionProcessRejectsBoundaryWork|TestGatewayRebindAdmissionProcessSerializesContenders|TestGatewayRebindAdmissionProcessRecoversCommittedClaim|TestGatewayRebindAdmissionProcessRecoversProtectedPreparation|TestRecoverGatewayRebindPreparedAdmissionTreatsEmptyCensusesCanonically|TestPrepareGatewayRebindLockedClaimsBeforeProtectedIntent|TestRecoverGatewayRebindPreparedAdmissionUsesRetainedClaimGenerationAfterPreCheckpointCrash)$' -count=1 -json -timeout=5m
```

All seven expected parents and four existing subcases passed; package elapsed
was 82.126 seconds and the process exited zero. Four intended source-file hashes,
the exact command/environment, full output and terminal result are retained in
`temp/m3-intent-crash-gate-20261009/baseline-*`. This is the unchanged baseline;
the new intent boundary and seam behavior require their own final results.
The baseline JSON output SHA-256 is
`2DC8EABDD4A02A51443F4DFEF89DF35E665F7B397A78D65A94C21641AF892418`.

## Final verification

After the fixture corrections below and an explicit source/security review,
the four changed Go files were frozen. The direct seam diagnostic passed first:

```text
go test ./internal/generatedingress -run '^TestPrepareGatewayRebindLockedIntentBoundaryRetainsFence$' -count=1 -json -timeout=2m
```

It ran the expected one parent with no skips or failures, exited zero, and took
1.924 seconds at the package level. Its output SHA-256 is
`0D42D6FD4847A46875F9DC0A632B5A6AF93616D5D63A49F6DC6603E16084EB47`.
The subsequent full native selection was:

```text
go test ./internal/generatedingress -run '^(TestGatewayRebindAdmissionProcessRejectsBoundaryWork|TestGatewayRebindAdmissionProcessSerializesContenders|TestGatewayRebindAdmissionProcessRecoversCommittedClaim|TestGatewayRebindAdmissionProcessRecoversProtectedPreparation|TestRecoverGatewayRebindPreparedAdmissionTreatsEmptyCensusesCanonically|TestPrepareGatewayRebindLockedClaimsBeforeProtectedIntent|TestRecoverGatewayRebindPreparedAdmissionUsesRetainedClaimGenerationAfterPreCheckpointCrash|TestPrepareGatewayRebindLockedIntentBoundaryRetainsFence)$' -count=1 -json -timeout=5m
```

All eight expected parents and five subcases passed with no skips or failures.
The package took 82.987 seconds and exited zero. Toolchain and read-only flags
matched the baseline. Native output SHA-256:
`3FA6794B0F180A0BAA9961C7E43C2693928E82DD37B45C8CCD79C8DF70C96DFC`.

The unchanged package was then cross-compiled with the Windows Go 1.27.0
toolchain, `GOOS=linux`, `GOARCH=amd64`, `CGO_ENABLED=0`,
`GOTOOLCHAIN=local` and the same read-only module flags:

```text
go test -c -o generatedingress-intent-crash-linux.test ./internal/generatedingress
```

The output binary was stored under the temporary evidence directory. Actual WSL
execution used the repository's `internal/generatedingress` working directory,
`-test.v -test.count=1 -test.timeout=5m` and the same anchored eight-parent
selection as `-test.run`. All eight parents and five subcases passed, no test
was skipped, and execution exited zero. The new `intent` crash subcase ran in
1.17 seconds; the direct seam test ran in 0.57 seconds. Linux output SHA-256:
`3D2548045ABB42BB2F9028315CB95B8C0F643C59FC5A4712051E0B448B231D18`.
This was real test execution, not a compile-only or skipped opt-in result.

Exact commands, environments, full logs, terminal results and discovered test
names are retained in `temp/m3-intent-crash-gate-20261009/final-*`, including
`final-platform-summary.json` and `final-source-freeze.json`. The frozen inputs
are:

| File under `internal/generatedingress` | SHA-256 |
| --- | --- |
| `manager.go` | `F10EBD86B2A6267EB6EB2CA166A9BCD1C9E2980EC5E711F7BFAD4D669FD23F15` |
| `gateway_rebind_cross_store_prepare.go` | `9B5C8268C3BB95FFD11017AFB206A04EEBFA023295713F4AB600CD27233C19B4` |
| `gateway_rebind_admission_process_test.go` | `693D8B8ADE5A5209CDC1BE11F8B52CC24C38A47A85C3A7794BC595BA8B5B0467` |
| `gateway_rebind_cross_store_prepare_test.go` | `248FFCA81BA40CD4881F0832B1FF537D5536D282A564E89E70214A9D04600BC7` |

`go vet ./internal/generatedingress` passed with the same local toolchain and
read-only flags, producing no output. A read-only `gofmt -d` of the four changed
Go files produced no diff. `git diff --check` passed; its output contained only
the checkout's line-ending warnings. Final file hashes match the frozen inputs
above. Independent security review accepted the private hook placement,
sanitized error path, retained fence/history and corrected real-repository test.
Final review and documentation gate records are retained separately so that
reviewing and building this document do not require changing their own inputs.

### Preserved initial candidate failure

The first native candidate selection ran before the direct seam-test fixture
correction. That process was allowed to finish and was reaped; source was not
edited while it ran. It exited one in 107.012 seconds. The direct
`TestPrepareGatewayRebindLockedIntentBoundaryRetainsFence` assertion counted
retained progress and terminal history already present in its fixture, rather
than a fresh prepared-claim prefix. Review also found that the callback assigned
the fake fence error which its later assertion checked. Neither assertion could
prove real SQL fence retention. The correction uses the existing real
repository/admission fixture, with an observational callback and fresh durable
SQL checks; it does not erase history or change the production seam.

The failed `green-native-*` records remain under
`temp/m3-intent-crash-gate-20261009/`. Output SHA-256:
`6C296A5BA11A982881324584C90DEAAA69B49D4635099E6A3B9BBB906398F5E0`.
Passing process cases in that attempt do not substitute for acceptance of the
corrected direct test or the final frozen candidate.

A second preliminary run used the real repository fixture but retained two
artificial manager-clock assignments from the earlier fake fixture. Those old
timestamps did not match the real repository's claim creation time; preparation
refused before the hook (`hookCalls=0`). Its seven other parents and five
subcases passed, but the candidate still failed. It was allowed to finish and
was reaped before the two test-only clock assignments were removed. The
`green-native-corrected-*` records remain separate; output SHA-256 is
`E10514FF25862ADD1F80549E91147F60E16D9F5ECDE04EE496EC8BAA4C5ECCFE`.
The final test uses the existing real fixture's default clocks. The production
hook and process assertions were unchanged through both fixture corrections.
After an explicit source/security review barrier, the direct parent is checked
first, followed by the final eight-parent native/Linux selection.

### Production compilation

With the two production hook files frozen, the following command passed in
5.719 seconds using Go 1.27.0 windows/amd64, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -buildvcs=false -p=1` and `CGO_ENABLED=0`:

```text
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

Output was empty and the process exited zero. All tracked production Go files
and `go.mod`/`go.sum` had identical hashes before and after the build; the
concurrent test-fixture correction was excluded from that production manifest.
The record is `temp/m3-intent-crash-production-build-20261009/result.json`,
SHA-256 `03A098714611921FCEE6C8C9D5D47F9DAEB909FE028ED8DC87DF2BA8EB4649A7`.
This proves compilation of the local working tree with the private hook; it is
not a clean-base build, test execution, runtime activation or deployment.

### Documentation baseline and correction

The isolated branch initially had no documentation dependencies installed.
`pnpm --dir docs install --frozen-lockfile --offline` succeeded using the
existing cache, Node v24.15.0 and pnpm 11.22.0. Package metadata and lockfile
hashes stayed unchanged. The install log SHA-256 is
`BE9E25D574D6C39ACE75D21753F9D0B8FEDA7F2078CF89D2F2A8BD45F3A0EE5E`.

The initial documentation workflow check passed, but the build failed on the
unchanged Docker-format example in `m3-rebind-cross-store-commit-evidence.md`.
VitePress treated its inline template braces as an interpolation expression.
The failed build log SHA-256 is
`A6D8D58E74D0EA65AC4CA273002C2BCEC90C3C9C47351DA3DB48A8A221822DAB`;
the documentation inputs stayed unchanged throughout that attempt. Accessibility
was not run after the failed build. Records are under
`temp/m3-intent-crash-docs-baseline-20261009/`.

The literal example is now fenced as code, using the same formatting correction
already present in the separately published runtime branch. This is a local
Markdown edit; no Git merge, history rewrite, code import or runtime change
occurred. The final documentation gate uses the existing repository commands:

```text
pnpm --dir docs check:workflow
pnpm --dir docs build
pnpm --dir docs check:accessibility
```

Full outputs, exit codes, tool versions and before/after hashes of all tracked
and new documentation inputs are retained separately under
`temp/m3-intent-crash-docs-final-20261009/`. Keeping the gate record separate
allows this evidence file itself to be included in the frozen build inputs.

## Rollback and remaining acceptance

Reverting this bounded seam and its tests restores the previous local fixture
behavior. Retain all SQL, protected intent/checkpoint/progress and immutable
history; do not delete them to force admission. An older binary may refuse state
it cannot prove. This local test extension does not authorize production
activation, a runtime branch integration, a GitHub merge or publication.

The remaining M3 contract still requires later durable-write/effect crash
boundaries, hosted Docker and race gates, and actual second-device LAN behavior,
including interface/address drift. Local simulated runtime tests cannot replace
those outcomes. M3 remains incomplete.
