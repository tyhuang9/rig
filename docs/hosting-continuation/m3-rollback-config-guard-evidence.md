# M3 rollback configuration guard verification

## Identity and scope

- Branch: `fix/hosting-m3-rollback-config-drift`
- Base: `9dc202c26c0faf43618246e2daeaff237e0b2453`
- Immutable commit: `90bfd85ac2b102461271c2a70df9cd652cb8dcdb`
- Package: `github.com/hostd/hostd/internal/generatedingress`
- Production file SHA-256: `458011EC9B931D3064E41D2D63D131B18D65E0AA8B04DD3D3528F302A520D2D8`
- Regression file SHA-256: `0ED6BB248D6CC78F2C486F731AE6F84FBBE2C6B6A0D37432570FB1DBAD61BCDD`
- Shared handover fixture SHA-256: `D694D84CA407C4FC589C6E35B7FD11F636E001724EC5C54FA54C7B429FD8F03B`
- Shared stage fixture SHA-256: `041F886B481CE3B7C505E7E20CAA7476B00B906963BD674534F75836C59EC1AB`
- Linux test binary SHA-256: `3856F6D9C6520D41CF36E71D8781C2A8EE9BF7EE36FAD3B61C6516CC11E13C20`

The source hashes above apply to every final-source run listed below. Earlier exploratory logs remain in this directory but are not final verification evidence.

## Changed files

- `internal/generatedingress/gateway_rebind_typed_handover_runtime.go`
  - Phase-aware strict stage and final config archive attestation before rollback authorization or container removal.
  - A rollback-only stable resource proof reads protected config bytes between two complete physical reads and rechecks context and authority.
  - Retained source effect remains the phase/digest authority while adopted or rollback-projected physical bindings are proved separately.
  - Forward `stableResources` behavior remains unchanged.
- `internal/generatedingress/gateway_rebind_typed_rollback_config_test.go`
  - Deterministic boundary, coordinator, replay, lost-acknowledgment, and zero-effect regressions.
- `internal/generatedingress/gateway_rebind_typed_handover_runtime_test.go`
- `internal/generatedingress/gateway_rebind_typed_stage_runtime_test.go`
  - Minimal archive error, truncation, and post-archive mutation controls for existing fixtures.

## Deterministic baseline failure

Command (Windows, with the toolchain/environment below):

```text
go test ./internal/generatedingress -run '^TestGatewayRebindTypedHandoverRuntimeRejectsStageConfigDriftDuringRollbackProof$' -count=1 -v
```

Result: exit 1. The exact copied archive control passed; the unexpected-member case failed because the pre-repair rollback proof accepted the archive. Raw output: `baseline-failing-direct-rollback-config.log`; status: `baseline-failing-direct-rollback-config.result`.

## Final checks

All Windows Go commands used:

```text
GOTOOLCHAIN=local
GOFLAGS=-mod=readonly -p=1 -buildvcs=false
CGO_ENABLED=0
Go=C:\Users\huang\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.7.windows-amd64\bin\go.exe
```

1. Phase contract and real p07 coordinator fallback:

```text
go test ./internal/generatedingress -run '^(TestGatewayRebindTypedHandoverRuntimeEnforcesRollbackConfigPhaseContract|TestGatewayRebindTypedHandoverRuntimeRetainsDriftedP07RollbackPrefix)$' -count=1 -v
```

Result: exit 0, 243.460s. Evidence: `focused-phase-p07-final-source-windows.log` and `.result`.

2. Persisted p13 final-create lost acknowledgment plus final-container rollback boundary matrix:

```text
go test ./internal/generatedingress -run '^(TestGatewayRebindTypedHandoverRuntimeAttestsAdoptedP13FinalWithoutRewritingHistory|TestGatewayRebindTypedHandoverRuntimeRejectsFinalConfigDriftBeforeRollbackRemoval)$' -count=1 -v
```

Result: exit 0, 320.788s. The p13 record is installed through the create-only progress store, re-scanned, guarded by repeated durable history and SQL snapshot reads, and verified unchanged after adoption and rollback projection proofs. Evidence: `focused-p13-and-final-boundaries-windows.log` and `.result`.

3. Direct stage boundary matrix, real p08 prefix, removal-gap replays, and unchanged forward drift controls:

```text
go test ./internal/generatedingress -run '^(TestGatewayRebindTypedHandoverRuntimeRejectsStageConfigDriftDuringRollbackProof|TestGatewayRebindTypedHandoverRuntimeRetainsDriftedP08RollbackPrefix|TestGatewayRebindTypedHandoverRuntimeRollsBackStageRemovalBeforeFinalCreate|TestGatewayRebindTypedHandoverRuntimeReplaysRollbackAfterFinalRemoval|TestGatewayRebindTypedStageRuntimeRejectsAlteredRestartAndLateBoundaryDrift|TestGatewayRebindTypedStageRuntimeRejectsAuthorityDriftAfterFinalArchive)$' -count=1 -v
```

Result: exit 0, 535.788s. Evidence: `focused-remaining-rollback-and-forward-windows.log` and `.result`.

4. Linux build:

```text
GOOS=linux GOARCH=amd64 go test -c ./internal/generatedingress -o C:\Users\huang\Documents\Projects\Rig\temp\m3-lan-external-db-20261015\rollback-config-guard-verification\generatedingress-linux.test
```

Result: exit 0. Evidence: `linux-test-binary-build.result` and the binary hash above.

5. Actual Linux execution through WSL:

```text
wsl.exe --cd /mnt/c/Users/huang/Documents/Projects/Rig/rig-hosting-m3-rollback-config-drift/internal/generatedingress --exec /mnt/c/Users/huang/Documents/Projects/Rig/temp/m3-lan-external-db-20261015/rollback-config-guard-verification/generatedingress-linux.test -test.run '^(TestGatewayRebindTypedHandoverRuntimeRejectsStageConfigDriftDuringRollbackProof|TestGatewayRebindTypedHandoverRuntimeEnforcesRollbackConfigPhaseContract|TestGatewayRebindTypedHandoverRuntimeRetainsDriftedP07RollbackPrefix|TestGatewayRebindTypedHandoverRuntimeRetainsDriftedP08RollbackPrefix|TestGatewayRebindTypedHandoverRuntimeAttestsAdoptedP13FinalWithoutRewritingHistory|TestGatewayRebindTypedHandoverRuntimeRejectsFinalConfigDriftBeforeRollbackRemoval|TestGatewayRebindTypedHandoverRuntimeRollsBackStageRemovalBeforeFinalCreate|TestGatewayRebindTypedHandoverRuntimeReplaysRollbackAfterFinalRemoval|TestGatewayRebindTypedStageRuntimeRejectsAlteredRestartAndLateBoundaryDrift|TestGatewayRebindTypedStageRuntimeRejectsAuthorityDriftAfterFinalArchive)$' -test.count=1 -test.v
```

Result: exit 0; all 10 affected parent tests passed. Evidence: `focused-complete-rollback-config-guard-linux-wsl.log` and `.result`.

6. Static and formatting checks:

```text
gofmt -d internal/generatedingress/gateway_rebind_typed_handover_runtime.go internal/generatedingress/gateway_rebind_typed_rollback_config_test.go internal/generatedingress/gateway_rebind_typed_handover_runtime_test.go internal/generatedingress/gateway_rebind_typed_stage_runtime_test.go
git diff --check
go vet ./internal/generatedingress
```

Result: no formatting diff, no whitespace errors, and vet exit 0. Evidence: `go-vet-generatedingress.result`.

## Review and limits

- Security review found and drove fixes for p07, p11, final-container removal, and p13/projection source-phase gaps. The reviewer issued a conditional GO for production hash `458011EC...`; root will run final security and integration review against the immutable commit. This is not a final delivery GO.
- The observed hosted K configuration-drift timeout remains causally unproven. These checks establish the independent rollback authorization repair only.
- Local Linux race compilation was unavailable. The actual Linux CGO-disabled binary passed under WSL.
- The root canonical generation check passed as recorded below. Fresh hosted Docker and Linux race gates remain required at the published head; earlier K execution is not transferred.
- Six new parent tests add hosted lane time. Existing Linux race and Windows K jobs already run near their package budget; hosted capacity is a publication risk to measure, not a reason to weaken or hide these regressions.

## Root canonical check — 2026-10-16 UTC

Root executed `pwsh -NoProfile -File scripts/check-generation.ps1` in this
worktree at tested source `90bfd85ac2b102461271c2a70df9cd652cb8dcdb`, using the
same Windows Go environment above. Exit code 0; controller contract test 0.210s.
This checked migration mirrors, generated OpenAPI artifacts, and registered
route/schema consistency. Exact captured output is `canonical-generation-final.log`
and exit status is `canonical-generation-final.result` in the external receipt directory.
No generated source was changed. This documentation is added after the check;
production and test blobs remain identical to the tested source.
Log SHA-256: `733c2deb6d7fd696c44643d1cfb3e39298e0b6b6cea26a917c2ca714f9a9c7a2`.
Result SHA-256: `bde294368bfed77c2cddf8cec271d398aee9cdbab3b26e1059281bd33adb0120`.
