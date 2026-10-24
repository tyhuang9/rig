# M4 current LAN commit-recovery fixture admission

Status: local Windows and Linux verification passed; hosted race and Docker
acceptance remain pending. This test-only branch starts from PR #209 at
`491219a6abb14e540a725759654ff96ca0044bf1`.

## Observed failure and repair

PR #209's two current-LAN hosted jobs failed the same four commit-recovery
tests and ten subcases. The fixture constructed a private `Manager` directly,
so its generated-domain admission view was absent. Production
`GrantGatewayV2LAN` correctly refused admission before creating the pending
commit marker; the tests therefore never reached their intended recovery
boundaries.

The fixture now prepares private controller directories with
`PrepareControllerDirectories`, then uses production `New` with its final
options and existing current-state repository. It asserts that construction
issues no daemon commands. Only the fixture's existing fail-stop and commit
barrier latches and physical driver are transferred to that admitted manager.
The four tests, ten subcases and their recovery assertions are retained.

Production admission, authority checks, runtime behavior and shared fixtures
are unchanged. No fabricated admission view or post-construction option
mutation is introduced.

## Executable evidence

On 2026-10-22, Go 1.26.7 built the generated-ingress test binary separately
for Windows/amd64 and Linux/amd64 with `CGO_ENABLED=0`, `GOTOOLCHAIN=local`
and `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`. The Linux binary ran natively
under WSL Ubuntu. Both binaries discovered exactly these four parents:

- `TestGatewayCurrentLANCommitRecoveryRejectsUnguardedDriver`
- `TestGatewayCurrentLANCommitRecoveryRetainsAuthorityAtCompletion`
- `TestGatewayCurrentLANCommitRecoveryStopsUncertainInitialWithdrawal`
- `TestGatewayCurrentLANCommitRecoveryWithdrawsAfterAuthorityLoss`

Each binary ran the anchored union of those names with `-test.count=1`,
`-test.timeout=10m` and `-test.v`: **4 parents and 10 subcases passed on each
OS, with zero failures or skips**. Windows took 249.68 seconds and Linux
28.72 seconds; these durations are observations, not a performance comparison.
The Windows grant/revocation preflight also passed (1 parent, 1 subcase).

Additional checks passed:

- Read-only `gofmt -l` on the changed Go file (empty output).
- `go vet ./internal/generatedingress` for both target operating systems.
- `pwsh -NoProfile -File scripts/check-generation.ps1`.
- `git diff --check 491219a6abb14e540a725759654ff96ca0044bf1`.

The successful local receipt is
`m4-current-lan-fixture-admission-verification/checks-20261022T224124Z/receipt.json`;
its SHA-256 is
`911e8fd483726f242f8d190821f25db5fd50c97c756d4d2e172018efc2fa8888`.
The complete Go source manifest stayed unchanged throughout verification.

| Evidence | SHA-256 |
| --- | --- |
| Changed test source | `f7401b207df12921f44887a28efd82a2ab2ec450b2572971b0672a12f4dc73b8` |
| Full source manifest | `0073017935eaa2f7e295ff61991f1626252d8bca3879a3da42ea9f9842e11166` |
| Windows raw test output | `61a7f8e4c37dc2d6eec4b212a50d8aa5ac52ac9fc050a4c6a4a7ed782534bd9c` |
| Linux raw test output | `a34fc8c0b5a5c704e6a8d5aa42921247e7fd87387166c4aac4f2659e8f624f42` |
| Windows binary | `a978e4bcf6dbe2d3295ddf5a6a3976e90ec5433665ea398273aad9cfba2b7589` |
| Linux binary | `ed06b1417cb5d982dca727dd083cdce6e994f6b89dba77229b1d7c0f19aad300` |

The initial local attempt used an ordinary temporary working directory and
failed Windows private-directory validation before exercising recovery.
That failure is retained in `checks-20261022T222734Z/receipt.json`, SHA-256
`39f0199eb5875b7f4abcd65fd5be58468d8b1b7a0b2219b966f0fe300e475052`.
The correction uses the existing production directory preparation helper;
it does not weaken the directory validator.

## Limits and rollback

Local verification used the existing fake physical driver. No local Docker
daemon or race-capable toolchain was available. Hosted current-LAN race jobs
must verify this candidate; these results do not establish Docker acceptance,
physical LAN qualification, overall M4 completion, or merge readiness.

Rollback is a revert of this fixture-only change and this evidence document.
There is no production data migration, deployment or runtime configuration
change.
