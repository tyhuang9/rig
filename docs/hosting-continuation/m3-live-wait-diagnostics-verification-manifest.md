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

## Fresh combined-source verification — 2026-10-16 UTC

The history-preserving local integration is
`96d5bec473d1002672afb3bfc1799382c88f1442`, with parents
`e790fa2fd1dfec432c39833360954505395ee0d3` (reviewed harness) and
`20efbf1aa4358be7cfef2d83356c8241949cd48a` (published runtime safeguard).
Its tree is `9b411900cf463897909d4b4fa2196e9eef98e633`.
The integration needed no conflict resolution. Its entire `internal` tree
matches the reviewed runtime parent; its entire `cmd` tree matches the reviewed
harness parent. The `scripts` and `.github` trees also match the runtime parent.

Root executed fresh checks against this combined head using the shared Go
environment and exact workflow selector above. The actual command arguments,
environment allowlist, exit statuses, captured stdout/stderr hashes, selected
parents and source identity are retained in the external
`runtime-wait-combined-verification/verification.json` receipt (SHA-256
`bdaccee81ad84c481a20efba78aaaf95b6e18df1f07613627384d391c163ade7`).

| Combined check | Actual result | Captured stdout SHA-256 |
| --- | --- | --- |
| Windows `go test -tags live_docker -json -count=1 -timeout=2m ./cmd/hostd -run <exact-selector>` | All 24 selected parents ran and passed once; zero fail/skip; package 12.302s; exit 0 | `0bf474810f2a88998ab3a5d91f5067e188d792fa77327a4acd75c47b0265e872` |
| Linux cross-compile followed by actual WSL execution with the same selector and two-minute cap | All 24 selected parents ran and passed once; zero fail/skip; exit 0 | `755920c60960bb2b316f6245bbc526fd48fda0f8dab20f3035e762bee0f5befd` |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Exit 0; controller contract test 0.644s | `01c2fcaecce7f4cdc252d31ebd2c0f142cda25fae2fa57b7b2d8127edf61eb3e` |
| Aggregate `git diff --check` against the runtime parent | Exit 0; empty output; worktree clean | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |

The combined Linux binary SHA-256 is
`483c9a37a4fead1f9d6762b35c91aac06905cfc86a6520032881f43f31d7a615`.
Linux stderr contains the existing negative TLS-control handshake rejection;
the corresponding test and package passed. It is not a production TLS result.
This section is a documentation-only addition after the checks; tested code
blobs are unchanged. Separate runtime fixture results remain bound to their
recorded source and are not described as fresh combined-head executions.

Fresh combined-source Docker cases, race checks and independent resource cleanup
remain required. Neither local controls nor source-tree equality accept the
hosted matrix, historical failure causes or M3.
