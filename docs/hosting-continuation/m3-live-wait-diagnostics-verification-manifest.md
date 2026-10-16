# M3 live-wait diagnostics verification manifest

This manifest transcribes completed local verification. It does not represent a
new execution. Source blobs identify the exact final harness content, and the
SHA-256 values identify receipts kept outside the checkout.

## Source blobs

| Path | Git blob |
| --- | --- |
| `cmd/hostd/gateway_rebind_process_live_test.go` | `0c5526ac22d89d3d293eef3cf92c561b013ecb1d` |
| `cmd/hostd/gateway_rebind_scenarios_live_test.go` | `5fcf1be471320b7a97777452610d1f70a1a85c1f` |
| `cmd/hostd/gateway_rebind_startup_deadline_live_test.go` | `df09959bee7a493be86a846b3f4d8f05648fd789` |
| `.github/workflows/hosting-gateway-v2-ci.yml` selector source | `94e7f9bdb814a14ecfe0522399a002b320ea99b7` |

## Shared environment and selector

```powershell
$goExe = 'C:\Users\huang\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.7.windows-amd64\bin\go.exe'
$env:GOTOOLCHAIN = 'local'
$env:GOFLAGS = '-mod=readonly -p=1 -buildvcs=false'
$env:CGO_ENABLED = '0'
$pattern = '^(TestGatewayRebindLiveRequiredManifestIsClosed|TestGatewayRebindLiveHarnessUsesSupportedDaemonAssembly|TestLiveHostdRebindDockerMutationClassifiesCopyDirection|TestAutoDeployStartObserverReportsOnlyEnabledLaunch|TestControllerRelayStartObserverReportsOnlyEnabledLaunch|TestGatewayRebindBackgroundLedgerAndDenyTransport|TestGatewayRebindBackgroundLedgerWriteFailureIsRetained|TestGatewayRebindBackgroundSentinelsUseRealRecoveryMethods|TestLiveGatewayRebindBackgroundLoopbackRejectedTLSRecordsAccept|TestLiveHostdRebindClosedDiagnosticControls|TestLiveHostdRebindLegacyV1OwnershipControls|TestGatewayStartupDiagnosticClosedCheckpointContract|TestGatewayStartupDiagnosticOuterStageWrappingPreservesCause|TestGatewayStartupDiagnosticRejectsForgedOrWrappedIngressMetadata|TestGatewayStartupDiagnosticWrapsRealStartupFailurePaths|TestGatewayStartupDiagnosticUsesDirectTaggedIngressResult|TestLiveHostdRebindClosedDiagnosticCheckpointRoster|TestGatewayStartupDiagnosticSnapshotStageAndCauseContract|TestGatewayStartupDiagnosticUsesDirectSnapshotResult|TestLiveHostdRebindClosedDiagnosticSnapshotCauseRoster|TestLiveHostdRebindPlanSuccessorProfile|TestLiveHostdRebindNormalReplayControls|TestLiveHostdRebindTerminalReceiptBindingControls|TestLiveHostdRebindChildStartupControls)$'
```

## Executions and receipts

| ID | Completed command and exit | Receipt SHA-256 |
| --- | --- | --- |
| Windows selector | `& $goExe test -tags live_docker -json -v -count=1 -timeout=2m ./cmd/hostd -run $pattern`; exit `0`, package PASS in 12.384s | `windows-tagged-hostd-ci-controls-final.jsonl` — `3eec55a3177e38d16e24fcbc0093cc9f082581193b58936eba4ef4cb81973d47` |
| Linux cross-compile | `$env:GOOS='linux'; $env:GOARCH='amd64'; & $goExe test -c -tags live_docker -o C:\Users\huang\Documents\Projects\Rig\temp\m3-lan-external-db-20261015\live-wait-verification\hostd-linux.test ./cmd/hostd`; exit `0`; `GOOS` and `GOARCH` were then removed | `hostd-linux.test` — `f68684a08a829d51963206502e290a16be29e807b856f165322c14c5bdf17c02` |
| WSL selector | `wsl.exe --cd /mnt/c/Users/huang/Documents/Projects/Rig/rig-hosting-m3-live-wait-diagnostics/cmd/hostd --exec /mnt/c/Users/huang/Documents/Projects/Rig/temp/m3-lan-external-db-20261015/live-wait-verification/hostd-linux.test -test.run $pattern -test.count=1 -test.v -test.timeout=2m`; exit `0`, PASS | `wsl-tagged-hostd-ci-controls-final.txt` — `be8737a84f64655931fba1ef6053bcfb88363967947805fc2364cd53d4d378a7` |
| Selector roster | Python 3.12 invoked as `python.exe -X utf8 -B -c <recorded roster validator>` over the Windows JSONL and WSL text receipts; exit `0`, 24 parents each ran and passed once on both systems | `hosted-selector-roster-validation-final.txt` — `19b330035b2e6ff5ba23f820bd1bc9b890738b15a01187732802b9e26327c980` |
| Canonical generation | With the shared environment and the approved Go directory prepended to `PATH`: `pwsh.exe -NoProfile -File .\scripts\check-generation.ps1`; exit `0`, `internal/controller` PASS in 0.661s | `windows-check-generation-final.txt` — `186068d96d71fb4d42f242bf8d518e1af988ce09055dbf85d75f45c95a763f1b` |

The canonical-generation receipt was completed before the final QA-only edits
to `cmd/hostd` live test files and these documents. Those edits do not alter
the migration, OpenAPI, or `internal/controller` inputs checked by that script.
The final-source Windows and WSL selector executions above cover the changed
test harness.

No command in this manifest starts Docker or uses the race detector.
