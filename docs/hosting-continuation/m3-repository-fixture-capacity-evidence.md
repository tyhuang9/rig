# M3 repository fixture capacity

Recorded 2026-10-09. Branch `feature/hosting-m3-repository-fixture-capacity`
starts from published `f49cf793bfae06f3d28fa45d4609a9489075cccb`.
Status: scoped native and Linux execution, static checks and production builds
passed. Full hosted acceptance and a hosted performance result remain pending.

## Purpose and boundaries

PR #137's hosted repository job `113941919520` reached the 40-minute package
limit in appaccess, autodeploy, controller and controllerrelay, then its overall
job limit. The timeout stacks were inside production database Open/Migrate
through four central test initializers. Repeated empty-database migration is a
candidate bottleneck; those stacks do not establish that caching the empty
fixture will make the complete hosted race suite pass. The generatedimage
static-server startup failure is a separate issue.

This unit provides one immutable, empty migrated database template per test
process. Each caller receives an exclusive independent file and connection,
opened again through production Open for migration-ledger checks and pragmas.
Only four central test fixture constructors change. Caller-owned directories,
cleanup, bootstrap/authentication and domain seed steps stay with their tests.
Other production Open calls, reopens and migration tests remain unchanged.

The helper must checkpoint and close the template before reading its bytes,
reject existing targets, sidecars and symlinks, and never copy seeded domain
state, reuse live connections or hard-link files. Only test sources may import
the helper; all four CLI dependency closures must exclude it. There are no
production schema, CI timeout, external database or managed database changes.

## Clean baseline

The four representative parents ran before edits at clean `f49cf79` using
Go 1.27.0 windows/amd64, `GOTOOLCHAIN=local` and
`GOFLAGS=-mod=readonly -buildvcs=false`:

```text
go test -p=1 -count=1 -json -timeout=40m -run '^(TestGatewayProfileApprovalCASReplayAndHostAddressBoundary|TestConfigureIsDefaultOffOwnerScopedAndRetiresLastSubscription|TestDeploymentMutationsRequireAuthenticationAndCSRF|TestIdentityAndKeyRepositoryEnforcesCanonicalCASState)$' ./internal/appaccess ./internal/autodeploy ./internal/controller ./internal/controllerrelay
```

The Go child exited 0. All four parents, ten subtests and four package outcomes
passed, with no failures or skips and unchanged source hashes. Wall time was
61.844 seconds, including the unit's cold build cache; parent durations were
0.41, 0.41, 0.53 and 0.46 seconds respectively. These wall times must not be used
to claim a fixture speedup without accounting for compilation.

Artifacts are retained under
`temp/m3-repository-fixture-capacity-20261009/` with prefix
`baseline-four-repository-fixtures`. Complete output SHA-256:
`46916FDF9430B5AD700A2B3C859A0EAC31F8372C5C2379D54E857DFC953DBA22`.

The original post-run wrapper incorrectly classified legitimate subtests and
full package import paths, so it exited 1 and recorded `passed: false` despite
the successful Go child. Its original output and result remain intact. A
separate `baseline-four-repository-fixtures.assessment.json` checks the same
immutable output against exact parents and canonical package paths and records
the corrected successful assessment. The baseline was not rerun to replace
that failed verifier result.

Documentation dependencies were installed using the declared pnpm 11.22.0
runtime and `pnpm --dir docs install --frozen-lockfile --offline`: exit 0,
3.625 seconds, with tracked and untracked documentation input hashes unchanged.
The original restricted attempt failed during the pnpm version preflight with
a registry fetch error, before installation or checks. It remains recorded in
`temp/m3-repository-fixture-docs-install-20261009/preflight-failure.json`.
The successful normal-user command/result and complete log are retained under
`temp/m3-repository-fixture-docs-install-normal-user-20261009/`; log SHA-256:
`A047CDBCD5534EE476EB0BEB39F724075FB470FD0F4B540A7952EEA555F1D0D1`.
This establishes dependency setup only, distinct from the final documentation
gates described below.

## Candidate results so far

The native helper gate ran all four discovered parents. Three passed, with
four artifact-refusal subcases; the symlink parent skipped because this Windows
execution lacked symlink privilege. There were no failures. Go child exit 0,
package 1.372 seconds, wall 3.031 seconds; source hashes remained unchanged.
Prefix `candidate-helper-databasefixture`; output SHA-256:
`E202D87449C3B177128CFEF27F2C84944E19EA65AF5703A58ACD329A3C72EFAC`.
Linux execution must exercise the symlink parent without a skip before local
acceptance is complete.

The equivalence test's direct setup sample measured fresh production Open at
380.1247 ms, the cold template plus first copy at 412.3814 ms, and a warm copy at
13.4312 ms. This is one local sample, not a prediction of hosted suite runtime.
Schema, table contents and pragmas matched. Eight concurrent copies retained
only their own committed row, discarded their rolled-back row after ordinary
production reopen, and left a subsequent copy empty.

The helper source hash was
`DEC05664549521622C08E668FFAC01815B7C31AC946C07F49B01BBCFCB98D38E`;
its test source hash was
`9FD59F52112B231B89F7BD41F8B65C24EF19E7D22DE4256DE68E9CDB4D71E4E4`.
Individual consumer and module hashes are retained in the command/result files.

Review then strengthened the equivalence census to include `sqlite_sequence`,
which stores the autoincrement state used by job and audit events, alongside
all application tables. This changed only the helper's test file, whose final
hash is `6CED5FDE366F6498B15D1F90C2A502CCED44A3D4604BD625077EBFB67C4AB1CA`.
The native helper rerun passed with the same sole Windows symlink-privilege
skip, exit 0, wall 3.000 seconds, unchanged source. Prefix
`candidate-helper-databasefixture-sqlite-sequence`; output SHA-256:
`E3CAFAFE222744BD9C0C64D325F4A7EAD60B1BAD750EC458083F36F4AFF1BDCD`.
The support implementation and four consumer inputs were unchanged, so the
consumer result below remains applicable.

The exact four consumer parents from the baseline passed on the candidate,
including all ten subtests and all four packages, without failure or skip.
Go child exit 0, wall 10.312 seconds, unchanged source. Package times were
0.762/0.792/0.848/0.738 seconds in the baseline command's order. Prefix
`candidate-four-repository-fixtures`; output SHA-256:
`794E77841B422BC25DB53D008C92E1DFBE6EE8C4E7ABB096FB08197453CE820D`.
The shorter wall time includes warm build-cache effects and is not an estimate
of the full hosted race suite's improvement.

### Actual Linux helper execution

All four helper parents and all four artifact-refusal subcases passed under
WSL, including symlink refusal, with no failure or skip. The Linux test binary
was cross-compiled from the final frozen source and actually executed; binary
SHA-256 `69E5FDB4DFEFD0CB51B2C41BCA8785840D8CA882218AB8F175FCFB64A9BA3FC0`.
Both chmod and execution exited 0, wall 5.110 seconds, with unchanged source.
Output SHA-256:
`5F81569DA84676AFA3F0B6D430E284B397958E3D4E3BEE618D49AB0599FAF296`.
The direct fresh/cold/warm setup sample was 471.710226/451.391600/8.912617 ms.
This plain Linux execution does not establish race-detector acceptance.

The first restricted WSL launch was denied before executing a test; compilation
had succeeded. The normal-user run reused that same verified binary. Its
post-run wrapper failed to trim subtest indentation and reported failure even
though every test passed. Original results remain under
`candidate-helper-symlink-wsl` and `candidate-helper-all-wsl`. The separate
`candidate-helper-all-wsl.assessment.json` verifies the actual exit and exact
parent/subtest outcomes from the immutable output. No behavior test was rerun
to replace a verifier-only failure.

The four selected consumer binaries also actually executed under WSL: four
parents and ten controller subtests passed, with no failure or skip. Every
execution exited 0; wall time was 7.547 seconds and source remained unchanged.
Their compile commands, executable hashes, exact test selections, complete
outputs and individual output hashes are retained in
`candidate-consumers-wsl-compile.result.json` and
`candidate-consumers-wsl-run.result.json`. The latter manifest's SHA-256 is
`216051E132014F4E318631D531BA3F44A1455B4B2366BCF2161F0B5B4D93C945`.

### Static checks and production dependency audit

Scoped vet passed with exit 0:

```text
go vet -p=1 ./internal/testsupport/databasefixture ./internal/appaccess ./internal/autodeploy ./internal/controller ./internal/controllerrelay
go list -deps ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

The second command passed and confirmed the helper is absent from all four
production CLI dependency closures. Formatter output was empty and diff-check
passed; the only Git output was line-ending warnings. The exact four imports
and initializer replacements were confirmed, preserving every other Open
call in those files. Source and module hashes remained unchanged.

The original static wrapper used a blanket truth check and incorrectly treated
`current_has_old_initializer: false` as failure, although absence is required.
The original result is preserved alongside the corrected
`static-verify-repository-fixture-capacity.assessment.json`; passed checks were
not rerun. The explicit CLI audit is retained separately in
`verify-cli-production-deps.result.json`.

Source inspection confirms the four helper imports are all in `_test.go`
files. The existing Windows and GitHub deployment workflows discover packages
with `go list ./...` and exclude only generated ingress from their repository
test commands, so the new helper package remains in those inventories. Their
package/job limits and strict aggregate checks are unchanged. This inspection
does not claim a new hosted run.

The canonical production build also passed, exit 0 in 43.844 seconds, with
empty output and every Go/module source hash unchanged:

```text
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

It used the same native Go toolchain and module cache with read-only module/VCS
flags and `-p=1`. The full command, real exit and source census are retained in
`build-production-cli.result.json`. Independent security and final integration
source review found no code blocker. These reviews are distinct from executable
verification and do not establish production or hosted acceptance.

## Verification plan and final documentation gates

- Compare fresh production Open, cold template and warm clone schema, every
  table's contents, migration ledger and connection pragmas. Normalize only
  migration `applied_at` timestamps; record timings without a pass threshold.
- Exercise eight concurrent isolated copies, independent commits and rollbacks,
  ordinary production reopen and refusal of existing targets without changes.
- Repeat the exact four consumer parents and execute helper and consumer tests
  under Linux in WSL, retaining real exits and discovered outcomes.
- Run scoped vet, CLI builds, import/dependency and four-callsite diff audits,
  formatting, documentation checks and independent source/security review.
- Run the complete hosted package/race gate after separate publication approval.

Before the local commit, the final documentation gates are
`pnpm --dir docs check:workflow`, `pnpm --dir docs build` and
`pnpm --dir docs check:accessibility`. Their real exits, complete logs and
before/after documentation source hashes are recorded in
`temp/m3-repository-fixture-docs-check-normal-user-20261009/result.json`.
The full package/race suites remain unrun locally; the focused checks above do
not substitute for those hosted gates. No new production UI or live external
service behavior is introduced by this test-support change.

## Rollback and remaining acceptance

Reverting this test-support unit restores repeated empty database creation.
It does not change deployed state or require data migration. No branch
integration, publication, PR merge or deployment is authorized by this record.
M3 remains incomplete, and no local result substitutes for the required hosted
Docker/race and physical LAN acceptance.
