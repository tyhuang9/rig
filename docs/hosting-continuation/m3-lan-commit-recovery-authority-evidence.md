# M3 LAN commit-recovery authority repair

Status: bounded local source/security review and targeted verification complete
on 2026-10-07. This closes the authority finding from the aggregate review;
it does not establish complete M3, live Docker, or Linux race acceptance.
Parent commit: `77de763` on `feature/hosting-m3-rebind-cross-store-runtime`.

## Finding and repair

The current-gateway recovery path validated terminal DB/actor authority once,
then could republish and clear uncertainty after that authority was revoked.
Its production physical factory is still closed. The native v2 recovery path
had an analogous pre-existing gap; both are repaired together because the public
recovery entry point must enforce the same contract for either selected lineage.

- Recheck the read-only, invocation-local authority callback through physical
  effects, lost acknowledgments, already-exact observations, and final proof.
  Preserve the initial observation identity and timestamp: repeated authorization
  does not claim another physical 404 observation.
- Require an explicitly guarded physical adapter for serving recovery. The native
  adapter guards every Docker command; the current adapter passes its combined
  selection/authority guard through its existing effect boundaries.
- On late denial, withdraw only exact-owned publication without requiring the
  revoked serving authority. If withdrawal is uncertain, attempt bounded exact
  stop and report `candidateMayBeLive` when that cannot be proved.
- Preserve retained raw grant authority and uncertainty history. Current-state
  denial after a durable clear appends a new withdrawal revision; it does not
  reinstall an old protected revision. Native cleanup retains its established
  quarantine protocol. An OS-lock release failure preserves the earlier safety
  diagnostic rather than overwriting possible live-candidate uncertainty.

No schema, dependency, controller API, factory activation, or hosting scope change
is included in this repair. External databases remain application-owned with
scoped runtime secrets; managed databases and Neon provisioning remain excluded.

## Executable evidence

Windows, Go 1.26.0. Commands use `-mod=readonly -buildvcs=false -p=1 -count=1`
and JSON output with saved exit codes. Logs are under
`C:/Users/huang/Documents/Projects/Rig/temp/`. Every expected top-level test was
discovered; package completions and process exits were checked and processes reaped.

| Log prefix (all suffixed `-20261007`) | Actual result | JSON SHA-256 |
| --- | --- | --- |
| `m3-current-lan-authority-red-execution` | Original current-path source: the regression failed for both grant and withdrawal recovery; 1 test/2 subcases failed, exit 1, 48.830s. | `81C14BC979A52CC9DFE564D7C7B0CCBE81986CD9988402148E58D9AA2E54FE5D` |
| `m3-native-lan-authority-overlay-red` | Original native recovery overlay: 2 tests/6 subcases failed, exit 1, 4.894s. | `290E5DDB5F03D5BD17A28A9E6A259E17B7138A1CD97664614484BA2A3AFF4B9E` |
| `m3-lan-authority-final` | Mixed run: 13 tests/24 subcases passed; 1 test/2 subcases failed on an incorrect retained-history test expectation, exit 1, 462.906s. | `2387B8590F3EEE7709907741C7BE189B3BFDC698B378A186295C3A20D6AA21D4` |
| `m3-lan-authority-corrected-boundary` | Corrected test alone: 1 test/4 subcases passed, exit 0, 98.038s. | `622B4B80518E445AEA036F217A9C7333486F149CB1F7EF8BA01B2C26670ADD0C` |
| `m3-lan-authority-controller` | Controller contracts: 4 tests/15 subcases passed, exit 0, 8.779s. | `3378963ACA7CBE810A41B7F4F6684A40374805E4E906211E02102D87D5B45339` |

The mixed run is a failed package run, not a green package result. Its only
failures expected `Apps.LAN` to be removed after durable-clear quarantine. The
existing protocol intentionally retains raw authority in `Apps` and
`Pending.Previous`, while `Pending.Proposed.LAN` is nil and physical publication
is withdrawn. Only those test assertions changed before the focused rerun:
they now require the exact retained raw binding, revision increment, uncertain
withdrawal marker, and physical compensation counts. Production source was
unchanged. Across these two runs all 14 selected ingress tests and their 26
distinct subcases have passing outcomes; there were no skips. Earlier compile
and fixture failures are not treated as acceptance.

The native RED used a Go overlay restoring only `gateway_v2_lan_grant.go` from
`2950279a99331b634340f231cc5b1d45469b3818`; it was not an untouched baseline
checkout. The new route helper compiled but was unused by the original recovery.
Overlay: `temp/native-lan-original-recovery-overlay/overlay.json`, SHA-256
`FAC07DA8E489B170240C25F3EE97FAD439C134937AC9E080AE1787B4DF0EFDC3`.
Original source SHA-256:
`183ABC9C8F9DF292B82D7EA83C95FEC01D4AC4E103EBE5AC7B99BF7817E0A78A`.

### Reproduction commands

The final ingress selection was:

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=15m -json ./internal/generatedingress -run '^(TestGatewayCurrentLANCommitRecovery.*|TestManagedGatewayCurrentLANCommitRecoveryPhysicalAuthority|TestGatewayCurrentStateMachineGrantActivationFailureRestoresBeforeAndRetainsMarker|TestManagedGatewayCurrentPhysicalDriverReconcilesOnlyExactSelectedMarker|TestManagedGatewayCurrentPhysicalDriverReconcilesLostAcknowledgement|TestGatewayV2LANCommitRecovery.*|TestApplyCommittedV2RoutesGuardedAuditsEveryDockerCommand)$'
```

The corrected focused rerun was:

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=5m -json ./internal/generatedingress -run '^TestGatewayCurrentLANCommitRecoveryRetainsAuthorityAtCompletion$'
```

Controller compatibility checks covered current session/actor authority, disable
intent, demoted approvers, and effective transfer-aware grant authority:

```text
go test -mod=readonly -buildvcs=false -p=1 -count=1 -timeout=5m -json ./internal/controller -run '^(TestLANGrantCommittedRecoveryUsesEffectiveAuthority|TestLANAppGrantRecoveryRequiresCurrentActorSessionBeforeRepublish|TestLANAppGrantRecoveryDoesNotRepublishAfterDisableIntent|TestLANAppGrantRejectsDemotedApproverBeforeTerminalCommit)$'
```

Also passed on the repaired source:

```text
go vet -mod=readonly -buildvcs=false -p=1 ./internal/generatedingress ./internal/controller ./cmd/hostd
go build -mod=readonly -buildvcs=false -p=1 ./...
gofmt -l <the eight changed or added Go files>
git diff --check
```

## Review, rollback, and limits

Independent source/security review accepted both repairs after the initial
withdrawal-stop and release-diagnostic issues were addressed. It traced guarded
serving effects, exact cleanup, retained history, and uncertainty propagation.
This is scoped review, not exhaustive execution of the aggregate runtime.

The repository/frontend checks at `2950279` remain recorded in
[aggregate verification](./m3-runtime-aggregate-verification.md). They were not
rerun wholesale after this ingress-only repair. The controller checks above,
targeted ingress cases, affected vet, and full Go build cover the final change.
No full ingress suite, actual Docker, Linux race, process-fault, or second-device
LAN acceptance is claimed. CodeRabbit CLI remains unavailable; no result is
substituted for it.

Keep production factories closed pending physical acceptance. Code rollback must
preserve all SQL and protected history; do not delete quarantine markers or raw
grants to make an older binary accept state. A safe older binary may refuse
retained state. Reverting this repair would reintroduce the authority gap.
Publication, GitHub merges, deployment, and controller restarts remain separate
actions requiring the user's authorization.
