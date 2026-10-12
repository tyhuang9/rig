# M3 current exact-owned mount stability

Status: focused native and actual WSL behavior, static checks, and production builds passed on frozen source. Source security and code review are GO. Documentation checks and final integration are recorded separately after this page is frozen. This follow-up does not close M3 acceptance.

## Source and purpose

The local branch `feature/hosting-m3-current-mount-stability` starts at published PR145 source `933bc81ff65cdd98252abfa0a83cdc5ebf768475`. Published and earlier diagnostic checkouts remain unchanged.

This repair treats the two independently validated, exact-owned Docker volume mounts as an unordered set at two remaining current-gateway comparison boundaries:

- repeated current physical observations used by the immutable current attestation; and
- independent terminal ownership reads by container name and immutable ID.

It preserves every mount field and every other observation field. It does not change ownership identity, runtime or liveness policy, resource census, authority, SQL fencing, history, effects, retries, or cleanup behavior.

## Hosted evidence and qualification

PR145 gateway job `114376996592` on source 933 completed all recorded stages successfully through sequence 17, `handover_committed`, and then refused with `route_reconciliation_required`. The last recorded container inspection succeeded with active context. The finite hosted log has no field-level post-P17 diagnostic and does not prove that mount order was the differing field.

The first post-P17 source candidates include the append trailing guard and forward confirmation before terminal installation, plus the later database-committed current physical attestation. The current physical observer contained a separate raw `reflect.DeepEqual(first, second)` over complete inventories whose final container still carried Docker's raw mount order. The terminal owned-stop path likewise compared its name and immutable-ID inspections before independently validating both observations. These are concrete source discrepancies: two exact, valid owned-mount observations can differ only in order and refuse.

The hosted cleanup attempt reported that it could not acquire the gateway lock. Source tracing shows that withdrawal can latch fail-stop and that raw-mode admission can reject a fail-stop or commit barrier before taking the mutex. This is a source-backed interpretation, not a cause established by the raw log. The log does not prove a lock leak, and source review found no reentrant acquisition or leak in this path. This repair does not weaken that admission rule or treat the cleanup symptom as the root cause.

Raw hosted response: `temp/pr145-933-hosted-acceptance-20261010/gateway/job-114376996592-response.json`, SHA256 `98036FFC6D5EDB4F7C1E0845D0051698F57786575FD8E9518A514CC195F3D2A7`.

## Repair boundary

The current physical observer first applies its existing full static final-container and runtime validation. It then clones and sorts the complete mount values with the existing canonical mount helper before the unchanged full-inventory equality comparison.

The terminal owned-stop observer now validates both the named and immutable-ID observations independently against the same final ownership facts. It retains exact runtime equality and the exact owned-container census, clone-sorts only each validated mount slice, and then compares the complete container observations. The later stronger liveness validation remains unchanged.

Caller-owned slices are not mutated. Present/absent state, non-mount container fields, ordered command values, runtime state, and all enclosing observations remain exact.

## Failing regression evidence

The tests-only `red.*` run used production files identical to source 933. Its two exact parents used independently decoded observations and changed only the second current physical mount order or the terminal name/ID mount order. Both reached their intended success assertion and failed under unchanged production after all read-count, source-nonmutation, and zero-effect preconditions passed:

```text
TestGatewayCurrentPhysicalObserveAcceptsOnlyOwnedMountOrderChange
TestGatewayCurrentTerminalOwnedInventoryAcceptsOnlyNamedIDMountOrderChange
```

The Go child PID was 32780, exit code 1. Both parents were discovered once and failed once; there were no passes or skips. Package time was 28.401 seconds and wrapper wall time was 82.531 seconds. `red.jsonl` SHA256 is `453BC9D9CDC3A1B3D0C5B6276849072695EAE76D6B648B87B2630F4B111A5AF3`.

This deterministic runner proves the raw-order defect in the source paths. It is not actual Docker evidence and does not establish the historical hosted differing field.

## Controls

The focused test file contains seven parents and 21 leaf subcases. It covers:

- positive current physical and terminal name/ID reversals with exact read counts, zero effects, and source nonmutation;
- added, removed, duplicate, and cross-owner mounts;
- ordered command drift;
- terminal name/ID presence mismatch;
- restarting, paused, dead, and running-without-effective-bindings liveness refusals; and
- an active `database_committed` current-attestation integration with an active claim and fence, an unchanged SQL startup snapshot by full structural equality, protected-history fingerprints, fence, fail-stop latch, and commit barrier.

Malformed named and immutable-ID observations are independently rejected before the comparison. Negative controls retain zero effects. The active database-committed control exercises the authority integration after the durable handover boundary without running the full external journey.

## Frozen verification

All receipts below are under `temp/m3-current-mount-stability-20261010/`. Verification used process-local official Go 1.26.7 with readonly module flags. Every step's pre/post 796-file Go/module manifest matched the frozen source.

| Gate | Observed result | Receipt |
| --- | --- | --- |
| Native Windows positives | Exit 0; exactly 3 parents passed once, no failure or skip; package 55.431 seconds, wrapper 63.140 seconds | `green.result.json`, `green.jsonl` |
| Linux cross-compile | Exit 0, 53.047 seconds | `wsl.cross_compile.exit.json` |
| Actual WSL | Exit 0; exactly 10 parents and 36 leaves passed; no failure, panic, or skip; 46.516 seconds | `wsl.result.json`, `wsl.actual_wsl.log` |
| Changed-file `gofmt -d` | Exit 0, empty output | `static.gofmt.exit.json` |
| `git diff --check` | Exit 0; only configured line-ending conversion notices | `static.diffcheck.exit.json` |
| `go vet ./internal/generatedingress ./cmd/hostd` | Exit 0, empty output, 60.500 seconds | `static.vet.exit.json` |
| Four CLI production build | Exit 0, empty output, 51.406 seconds | `static.build.exit.json` |

The native selection contained the current physical positive, terminal name/ID positive, and active database-committed integration. Actual WSL ran all seven new parents plus these adjacent safety controls:

```text
TestGatewayRebindCurrentAttestationRefusesActiveAuthorityDrift
TestGatewayCurrentTerminalEmergencyStopsDespiteCorruptCurrentBundle
TestGatewayCurrentTerminalEmergencyRefusesForeignOrAmbiguousOwnership
```

Windows Go cross-compiled the Linux test binary, which then executed inside actual WSL. Live gateway opt-ins were removed. The Linux binary SHA256 is `7BD63BE74B98AD874EB2A555741F230370140C8FDA4EED19FBF0DBB4F46FCCA9`.

The production build command was:

```text
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

| Artifact | SHA256 |
| --- | --- |
| `green.jsonl` | `1F0AAEFD06896CA4EBEDABB145EDC0AED0B2E7E1CD6DB8393A16130100A1F60E` |
| `green.result.json` | `09F555787E9653F314150876C25E36CEAA9A98819FE735C9846D4B5ADF67912E` |
| `wsl.actual_wsl.log` | `58A38B9046AF5595CEAF098ADF43A6BECA3787EBB112B46DCA0F209F7349FE12` |
| `wsl.result.json` | `2FB4E7601DEE6A838F09C8FC94B1DA02285CC1EDD4E04E650B04F8FF7E71DAF9` |
| `static.result.json` | `C01CBF4ADD433B11895A423D70FCF243682A1A816F7A34C68F8D8FC77285BA3F` |

The retained native session was 17836 with child PID 44944. The WSL wrapper session was 60306: cross-compile child PID 31948 and actual WSL child PID 8576 both exited 0. The static/build wrapper session was 18110 and its terminal exit 0 was directly collected.

`root-ci-selection.json` is a static selector assessment, not compiled discovery or CI execution. It maps each of the seven new parents to exactly one of the 18 existing generated-ingress groups and records the three workflow files plus shared selector unchanged. Its rejected first extraction omitted the digit in `gateway-v2`; the corrected receipt records that metadata-only attempt and no Go execution.

## Documentation and final disposition

Root-owned documentation commands run after this page freezes:

```text
pnpm --dir docs check:workflow
pnpm --dir docs build
pnpm --dir docs check:accessibility
```

Their outcome is retained separately without rewriting this page to include its own check result. Security, code-review, and final-integrator dispositions are retained with the orchestration evidence. None of these local results authorizes publication, merge, activation, or hosted acceptance.

## Limits and rollback

No local native Docker daemon was used. Actual Docker behavior and the historical hosted post-P17 differing field remain hosted qualifications. The deterministic runner and actual WSL test binary do not substitute for an external typed Docker journey, a killed full controller, the full durable-write/effect crash matrix, second-device LAN proof, or factory activation acceptance.

No timeout, CI, schema, module, deployment, or public activation change is included. Rollback is a normal revert of the two production files and focused regression test; there is no persisted data-format change or migration.
