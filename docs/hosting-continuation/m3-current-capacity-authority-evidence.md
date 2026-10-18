# M3 capacity admission after committed gateway rebind

Date: 2026-10-18. Base: `9cdbd109cc3aeceab42a92d596eff21bead6451d`.

## Defect and scope

`Manager.Snapshot` previously selected committed upgrade history directly. That
scanner intentionally refuses native rebind artifacts; its fallback only admits
the predecessor after a native abort. Consequently, a completed native rebind
commit could not obtain a capacity snapshot for a subsequent deployment.

The repair resolves current SQL/protected authority under the existing gateway
lock. For a selected rebind it uses the existing physical attestation, requires
stable serving, and runs the fixed capacity command against the attested immutable
final-container ID. Upgrade, native-abort predecessor, and v1 fallback code is
unchanged. No history, authority, recovery, secret, or route writes are added.

The pending-route Docker scenario now explicitly requires its historical rebind
to have committed, and emits only a fixed disposition marker. The shared helper
and scenarios that intentionally accept abort remain unchanged.

## Reproduction and prior hosted evidence

The pre-fix regression executed the real coordinator/SQLite/protected-state
fixture, required exact COMMIT and released-fence/current-selection invariants,
then failed at `Snapshot` with `route_reconciliation_required`. Exit 1; test
20.77 seconds, package 20.886 seconds. Its test-source SHA-256 was
`2c2b0a1b8eb38a17b93380edc1b19ef648c82e84b70be35cd02879dfceb2f99b`;
raw JSON output SHA-256 was
`b17a3584bd367ab3a9c1146dd0e924ade229ad78dad64064e25446098e172535`.
The error ended that test before its later probe/history assertions.

The same positive behavior now passes alongside refusal and compatibility
controls. It requires one attestation, exactly one immutable-ID capacity probe,
the expected capacity values, and unchanged SQL/protected history. Ten refusal
cases require zero probes: active newer rebind, SQL head drift, terminal mismatch,
pending route, LAN recovery, foreign binding, ambiguous or non-serving physical
outcome, and missing or malformed final container.

[Hosted run 39103009798](https://github.com/tyhuang9/rig/actions/runs/39103009798)
at the base passed 62 of 63 matrix cases. Pending-route restart failed before
publication with `internal_error`; all eight independent cleanups succeeded.
The aggregate correctly failed. That artifact does not retain the historical
rebind disposition, so attribution of that earlier failure to this defect remains
conditional. The new explicit COMMIT prerequisite makes the next run exercise
the repaired path directly.

## Local verification

Pinned Go 1.26.7; `GOTOOLCHAIN=local`,
`GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`, `CGO_ENABLED=0`.

The exact capacity selection is reproducible in PowerShell:

```powershell
$capacityTests = @(
  'TestGeneratedIngressCapacitySnapshotUsesCommittedRebindCurrentAuthority'
  'TestGeneratedIngressCapacitySnapshotRefusesUnstableRebindCurrentAuthority'
  'TestGeneratedIngressCapacitySnapshotPreservesNativeAbortPredecessorAuthority'
  'TestProvisionAndCapacitySnapshotUsePinnedDockerDataPlane'
  'TestCapacitySnapshotPreservesGenuinelyLowCapacity'
  'TestLowIngressCapacityBecomesReplacementCapacityDiagnostic'
  'TestCommittedV2ManagerSwitchObserveRecoverAndSnapshot'
  'TestOptionalGatewayCurrentSelectionRejectsChangesAndOrphans'
  'TestReadGatewayCurrentSelectionLockedRevalidatesSQLAroundProtectedRead'
  'TestManagedGatewayCurrentPhysicalRuntimeAttestationRequiresExactIngressMembership'
  'TestGatewayCurrentPhysicalAttestationRejectsRehashedLegacyTerminalSummary'
  'TestManagedGatewayCurrentPhysicalRuntimeBindsImmutableContainerAndDynamicNetworks'
)
$capacityPattern = '^(' + ($capacityTests -join '|') + ')$'
go test -json -count=1 -timeout=10m ./internal/generatedingress -run $capacityPattern
```

| Check | Result |
| --- | --- |
| Windows capacity selection | 12 parents / 38 named tests each ran and passed once; no failures or skips; 390.996 seconds wall time |
| Actual WSL execution of the same Linux test binary selection | The same 12 parents / 38 names each ran and passed once; no failures or skips; 29.700 seconds execution wall time |
| Existing 25-parent live-test preflight and compilation | 329 named controls each ran and passed once; no failures or skips; 28.194 seconds wall time |
| Formatting and diff check | `gofmt -l` returned no files; `git diff --check` passed |
| Vet | `go vet -tags live_docker ./internal/generatedingress ./cmd/hostd` passed |
| Canonical generation | `pwsh -NoProfile -File scripts/check-generation.ps1` passed |

Windows raw output SHA-256:
`56c1f6a36649d511d4c62f2a157327d42828ebf16862c382cbab7c8f0ad3f369`.
WSL raw output SHA-256:
`d4e9ef18d845da48c33916e32b4c8c7043cef58be4dc841da8f98a6e78da418c`.
The Linux test binary SHA-256 is
`a0a3a2bd828176a60a792e4698e0d9db1762f49c186a87197bf1e34cdef2f2a2`;
it was cross-compiled with the pinned toolchain and executed under WSL from the
package directory, using the same selection and ten-minute test deadline.
Controller preflight raw output SHA-256:
`9cff1c4eec0a3baba25a1fee8109c3da74435f4fc32bb49fb64ddbd141b49738`.
That command was `go test -tags live_docker -json -count=1 -timeout=2m
./cmd/hostd -run <exact workflow test_pattern>`, using the existing 25-parent
preflight selection from `hosting-gateway-v2-ci.yml`. The live Docker scenario
itself was compiled but not executed locally.
All verification records bind the frozen source bytes before and after execution.
Independent security/code and QA reviews found no must-fix issue.

## Remaining acceptance and rollback

These controls exercise real SQLite/protected state with a simulated physical
driver. They do not establish actual Docker attestation, Linux race behavior,
the full crash matrix, a physical second-device LAN journey, or M3 completion.
Hosted CI must verify the published repaired revision, including the explicit
COMMIT marker, successful pending-route recovery, all 63 cases, and independent
cleanup. Previous failures remain part of the evidence.

Rollback is a normal reviewed revert of this repair; it requires no data or
history migration. It restores the prior capacity-admission limitation after a
committed rebind. No deployment or controller activation is part of this change.
