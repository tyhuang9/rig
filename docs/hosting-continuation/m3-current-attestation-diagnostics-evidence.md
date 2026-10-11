# M3 current-attestation refusal diagnostics

Status: focused local behavioral verification, scoped vet and four CLI builds passed; documentation verification is recorded in the external receipt below. This change does not repair the observed Docker refusal or complete M3 acceptance.

## Observed boundary and source

The isolated branch starts at published PR145 head `c909b4de6290d8ed220ee8e31b53a0bb5c9025b3`. Its hosted merge `5a8654f` has the same source tree. Docker commit job `114513969207` failed in 151.94 seconds; independent PR146 commit job `114514089631` failed in 148.35 seconds on c909 plus the TLS diagnostic change. Both recorded:

- Durable sequence 17, `handover_committed`; all stage methods succeeded.
- First coordinator failure `current_attestation`, after successful reconciliation and forward confirmation.
- SQL `database_committed`, commit terminal receipt and current artifact present, successor authority selected.

The latest successful container inspection can belong to later withdrawal. It does not identify the first refusing predicate. Cleanup reported that it could not acquire the gateway lock; that message alone does not establish a leaked mutex. Both same-source rollback journeys passed with independent cleanup: PR145 job `114513969442` in 135.28 seconds and PR146 job `114514089674` in 166.68 seconds.

Raw hosted evidence remains under `temp/pr145-c909-pr146-7fd-hosted-acceptance-20261011/`. The PR146 commit response SHA256 is `81EF26965260A1C5F41B8C5F6C0044C742C3F10F714A1D9E0288BC7B5BD1BF53`.

The refusal returns through `attestCommittedCurrentLocked` in `gateway_rebind_cross_store_commit.go`. The existing test decorator locates that method, but it cannot distinguish its internal authority/history guard from physical inspection, restart archive validation, configuration comparison, probe proof, or repeated-observation stability. The runtime interface exposes the complete observation, not each internal validation branch. Duplicating those validators or rereading Docker would change the evidence and risk diverging from the real path.

## Implementation and invariants

The existing concrete `*Error` gains an unexported finite checkpoint. The checkpoint is attached only at existing current-attestation refusal branches in the manager, managed physical driver, and its runtime observer/read/attestation functions. Existing fresh-error collapse points preserve a recognized checkpoint; direct error returns and the final canonical digest return remain direct.

The public error type, `Code`, `Error()` text, JSON fields, cancellation classification, and existing `errors.Is`/`errors.As` behavior are preserved. There is no new unwrap chain. The extractor accepts only a direct `*Error`; wrapped errors and arbitrary error implementations cannot inject diagnostic labels. Invalid enum values become `unknown`. Each replacement error is fresh and retains no upstream cause, raw error text, configuration, identifier, path, address, digest, or command output. Private structural equality can differ because the additional unexported field carries evidence.

The change preserves the original short-circuit order and adds no reads, Docker commands, effects, validators, callbacks, global or Manager state, retries, authority, or recovery behavior. Stop, reconcile, rollback, SQL/schema, immutable history, factory activation, module and CI configuration are unchanged.

The existing live-test coordinator recorder captures the first failed current-attestation checkpoint under its mutex before deferred withdrawal or joined errors can obscure it. A first `unknown` remains authoritative even if a later failure has a recognized label. The original delegate is called once and its return values/errors remain unchanged. Only a boolean and finite checkpoint string are added to the existing failure log.

## Real SQL and composition control

The new two-app test uses the existing real SQL/protected/concrete composition fixture. It adds the second loopback app before claim creation, retaining one LAN roster entry and two runtime heads/components and application networks. The same executor that performed P11 final configuration and P17 handover supplies the observed current state. The test independently compares the rendered final bytes with the selected current baseline and executor live/restart bytes; it does not initialize those bytes from the expected current baseline.

At the genuine DatabaseCommitted boundary, the control compares SQL startup snapshots, protected history, both safety latches and effect count around the unchanged current-attestation delegate. It asserts the complete P17 history and the two-app census. The observer entry counter identifies the managed-driver boundary; it does not by itself prove a physical read occurred.

The initial locator failed before that boundary because the second app's seed left its job running and deployment preparing. Production quiescence correctly refused. The fixture correction settles exactly that deployment and its linked job from their expected states, requiring one affected row each before claim construction. It does not bypass a fence or change production behavior.

With production still unchanged at c909, the corrected locator passed its sole parent in actual WSL in 9.78 seconds (10.000 seconds wall); crosscompile and discovery also exited zero. All 798 Go/module inputs remained unchanged. The corrected receipt is `temp/m3-current-attestation-repro-20261011/repaired-current-attestation-boundary.result.json`, SHA256 `3B2F1B01AFAF114C0B49ADDF31DB9EFCD76C9D03AD3D16C979C03E077ECF5EF5`; raw log SHA256 `9F98FF70C172D88D849705FAD8E17DE0EF2E4F5AD421A7CEC2E80CAB0EFC181C`. The original failed locator and a later pre-launch output-name collision are retained separately.

This passing deterministic control narrows the missing evidence. It does not reproduce the actual Docker refusal or establish a differing runtime field.

## Focused verification

The final selection has five exact parents and nine leaf subcases:

| Parent | Purpose | Leaf subcases | Existing CI partition |
| --- | --- | ---: | --- |
| `TestGatewayCurrentAttestationDiagnosticErrorContract` | Public error/cancellation/JSON/Is/As compatibility | 2 | `current-serving` |
| `TestGatewayCurrentAttestationDiagnosticErrorSanitizesUnknownAndWrappedCauses` | Direct checkpoint, wrapped/custom cause rejection, invalid clamp | 4 | `current-serving` |
| `TestGatewayCurrentAttestationDiagnosticLeavesCollapseWithoutMutation` | Real DatabaseCommitted census guard, stopped serving-proof refusal, distinct valid second proof | 3 | `current-serving` |
| `TestLiveGatewayRebindRuntimeCoordinatorRetainsFirstCurrentAttestationCheckpoint` | Earliest unknown attestation failure survives later known failures | 0 | `ingress-remainder` |
| `TestGatewayRebindCurrentAttestationCompositionBoundaryRetainsTwoAppCommittedCensus` | Two-app P11/P17/DatabaseCommitted success and nonmutation | 0 | `rebind-current` |

The real SQL negative controls retain the durable fence, compare SQL startup and protected-history snapshots, require zero effects and clear post-refusal latches, and assert zero, one, or two observations respectively. The stopped runtime legitimately produces an observation before the serving-proof check refuses it.

The first annotated-source run compiled and discovered all five parents, but its stopped-runtime row incorrectly expected `driver_first_observation`. Actual evidence was `driver_first_proof`, one observation, zero effects, a stopped container, and clear latches. Four parents passed; the whole run failed and remains failed. The only correction changes that row's label and expected enum. Production source is unchanged by this correction. Original receipt/log prefix: `temp/m3-current-attestation-diagnostic-20261011/final-current-attestation-diagnostic`; raw log SHA256 `AFDAAD2AE62D0F6CEC73EF1DB60714D3D8C6242787B3BE02187C7E92191F7010`.

### Final Linux execution

The commands selected Go 1.26.7 with process-local `GOTOOLCHAIN=go1.26.7` and `GOFLAGS=-mod=readonly -buildvcs=false -p=1`. The Windows compiler produced a Linux/amd64 test binary with `CGO_ENABLED=0`; WSL then discovered and executed that binary from the package directory. This is actual Linux execution, not a crosscompile-only result or Docker execution.

```text
go test -c -o <retained-linux-binary> ./internal/generatedingress
<retained-linux-binary> -test.list=<exact-five-parent-regex>
<retained-linux-binary> -test.v -test.count=1 -test.timeout=20m -test.run=<exact-five-parent-regex>
```

The exact binary path and anchored regex containing the five table entries are retained in `final-current-attestation-diagnostic-proof.command.json`. Crosscompile, discovery and WSL execution exited zero in 8.015, 3.890 and 15.860 seconds respectively. All five parents and nine named leaf subcases ran and passed exactly once; there were no skips, failures or panics. The two-app composition parent passed in 9.17 seconds, and the three real SQL refusal cases passed in 6.26 seconds together. All 800 Go/module inputs matched before and after every command.

Artifacts are in `temp/m3-current-attestation-diagnostic-20261011/`:

| Receipt | Evidence |
| --- | --- |
| `final-current-attestation-diagnostic-proof.result.json` | Original wrapper assessment is false; individual command exits and source pins are retained. SHA256 `08D6DB5F66F03C8840C14443576EE49CEB9BE01A9DDFFCCDE60D3B1E04F92DE2` |
| `final-current-attestation-diagnostic-proof.actual_wsl.log` | Actual passing Linux output, SHA256 `BC7056F2E19A43F65AE9120C570BE2ACC101FD906CF07E4C62612FAA78EDC4D6` |
| `final-current-attestation-diagnostic-proof.unordered-assessment-v2.json` | Corrected exact-set, unique-event, exit and source assessment; SHA256 `774C1DE2702E368943FD6D6F1E13F7FD006264E62EEE609CAFC81657E8663A71` |
| `root-proof-execution-assessment.json` | Independent direct-raw-log assessment of all fourteen unique RUN/PASS events, discovery, exits and source pins |

The original verifier incorrectly required declaration order to equal the manually listed parent order. Its first separate assessment then omitted indented subtest PASS lines. Both false assessments remain untouched. The corrected and independent assessments parse the same immutable passing log; no tests were rerun for these metadata errors.

### Native and production checks

The native command executed the three public-error and recorder parents, retaining their full six leaf subcases:

```powershell
go test -count=1 -json -timeout=5m -run '^(TestGatewayCurrentAttestationDiagnosticErrorContract|TestGatewayCurrentAttestationDiagnosticErrorSanitizesUnknownAndWrappedCauses|TestLiveGatewayRebindRuntimeCoordinatorRetainsFirstCurrentAttestationCheckpoint)$' ./internal/generatedingress
go vet ./internal/generatedingress
go build -trimpath ./cmd/hostd
go build -trimpath ./cmd/hostctl
go build -trimpath ./cmd/rig-relay
go build -trimpath ./cmd/rig-relay-probe
```

All six commands exited zero with unchanged 800-file Go/module manifests. Native test package time was 1.243 seconds (53.734 seconds wall including compilation), with exact three-parent/six-leaf passes and no skips or failures. Scoped vet took 15.437 seconds. The four builds took 40.797, 2.000, 5.938 and 1.266 seconds; vet and build logs were empty.

The result is `native-current-attestation-diagnostic.result.json` in the same artifact directory, SHA256 `176202344E952AF1B67B081F9B77641FE7E1F4FB7F0B23E26181A9DF5D6BDF0C`. Native JSON log SHA256 is `D22847FCFE95DA1B9ED948D9CA8E9A64E6C79B6682BDEB735FCE40B77BAC5CE4`.

`git diff --check` passed. The first aggregate `gofmt -l` assessment reported four existing-file CRLF differences and remains preserved as `orchestrator-final-static.json`. A separate read-only check fed canonical LF bytes from all nine changed Go files to the same formatter; each output exactly matched its input, with no working-file change. This is recorded in `orchestrator-canonical-format-assessment.json`, not a hidden replacement of the first result. The four generated CLI executables were moved from the checkout into this unit's temporary artifact directory with their hashes retained; all 800 Go/module pins still match.

## Review and acceptance limits

Independent production compatibility/security and recorder/test reviews returned GO, retained in `temp/m3-current-attestation-repro-20261011/source-review.md` and `test-review.md`. They are source reviews, not test execution. The static selector receipt `ci-selection.json` maps each of the five parents to exactly one of the eighteen existing required ingress partitions in Linux race and Windows plain workflows; all selector/workflow blobs are unchanged. Initial read-only assessment attempts stopped on parser case-arm inventory mistakes before any source mutation; the corrected receipt records that metadata history.

The documentation commands are `pnpm --dir docs check:workflow`, `pnpm --dir docs build`, and `pnpm --dir docs check:accessibility`. Their observed result belongs in `temp/m3-current-attestation-diagnostics-docs-check-final-20261011/result.json`; this page does not preclaim its own pending check.

No local Docker daemon is available. Actual hosted typed commit, replay, and independent cleanup remain required on the eventual published head. The new labels have not yet identified the historical refusing predicate, and the change cannot be presented as a Docker runtime fix. Public factories remain dormant. Full supported-controller crash recovery at every durable write/effect and physical second-device LAN acceptance remain incomplete M3 gates.

The separate Windows history fixture failed before its scenario subtests on PR145 and passed with all seven subcases on PR146; that mixed outcome has no established cause or fix in this unit. TLS and unrelated CI capacity changes are outside this branch.
