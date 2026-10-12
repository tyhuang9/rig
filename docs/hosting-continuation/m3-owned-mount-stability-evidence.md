# M3 exact-owned mount stability

Status: focused native and actual WSL behavior, static checks, and production build passed on frozen source. Source security and code reviews are GO. Documentation and final integration dispositions are recorded separately after this page is frozen. This follow-up does not close M3 acceptance.

## Source and purpose

The local branch `feature/hosting-m3-owned-mount-stability` starts at published PR145 source `87a0bad86f9c7d17c924d870bedc4ef7078f6457`. The published checkout remains unchanged.

The repair treats the two independently validated, exact-owned Docker volume mounts as an unordered set for stable observation comparison. It preserves every mount field and all other physical observation fields. It does not change ownership, resource identity, configuration, listener, authority, history, recovery, or effect ordering.

## Hosted failure evidence

PR145 tested merge `bafc6c1` has the exact source tree `6852283c984a92a7095e68b4a3136cd13a9fa588` of 87. Gateway workflow 38100228636 completed with 13 successful jobs and two failed jobs:

| Job | Observed boundary | Qualification |
| --- | --- | --- |
| 114354469848, typed commit | First failed delegate `copy_stage_config`, operation `stable`, checkpoint `stable_equality`; retained sequence 8, rollback intent | The fixed diagnostic locates a comparison, not the differing field. Later successful `network_ls` is not the failure cause. |
| 114354469809, typed rollback | All three recorded stage methods succeeded; retained sequence 12, final configuration copied; outer refusal after successful stage method | This is a different interval. The log does not prove which subsequent physical predicate refused. |

Both jobs retained resources when exact terminal cleanup could not be established. Raw decoded responses and the source identity assessment are retained under `temp/pr145-hosted-failure-investigation/`.

## Source discrepancy and repair boundary

The exact-owned stage mount validator accepts either order of the two required mounts. Ordinary gateway V2 code already accounts for Docker returning these mounts in a different order. Typed-stage and typed-handover stability comparisons nevertheless compare the raw mount slices with `reflect.DeepEqual`.

Two individually valid observations can therefore disagree solely because of mount order. This is a deterministic source discrepancy consistent with the recorded refusal boundaries. It does not establish that mount order caused every hosted failure.

The reviewed repair validates each present container against its immutable mount identity, clones and sorts the complete mount values by their validated unique volume names, and compares the complete enclosing observations. It also clones a nested native/upgrade predecessor observation before canonicalizing its final-container mounts. Original slices and pointers must remain unchanged.

The five changed comparison sites are typed-stage stable observation, handover stable resource observation, handover first/second and second/final-third physical observation, and rollback-prefix stage observation. There is no global Docker parser change, additional read, guard, retry, effect, or authority callback. Two identically malformed mount observations can now refuse earlier than the previous raw equality, before a later read-only guard. The negative controls require refusal and zero effects; they do not claim identical guard counts for malformed input.

## Baseline

The clean-base Windows baseline used process-local Go 1.26.7 and readonly module flags. Its exact selection was:

```text
TestStableGatewayV2ContainerMountsAcceptsOnlyExactMountOrderChange
TestGatewayRebindTypedStageRuntimeCommandsStartReplayAndCopyFinalConfig
TestGatewayRebindTypedHandoverRuntimeReconcilesActualFinalCreate
```

The saved Go JSON records exactly these three parents passing and package PASS in 54.212 seconds, with no test failure or skip. Original command and output: `temp/m3-owned-mount-stability-20261010/baseline.command.json` and `baseline.jsonl`.

The wrapper then failed during postprocessing because its parent-pass parser accessed a `Test` field on the package PASS event. This wrapper failure is retained separately. The child was awaited by `subprocess.run`; its numeric exit code was not persisted before the parser exception and must not be inferred from the package event. No behavioral rerun was performed solely for this metadata failure. The original baseline manifest covers production Go and module files, accompanied by clean exact-base Git state; it is not described as a full prelaunch test-source manifest.

## Failing regression evidence

The tests-only first run, `red.*`, exited 1. The P7 case reached the intended `stable/stable_equality` refusal after exactly two reads and before any archive, copy, or effect. Its three P12 cases instead stopped before their injected order change: the test interceptor had re-encoded unrelated endpoint responses using the gateway-container shape. That whole failed run is preserved and is not counted as P12 comparison proof.

After correcting only the test interceptor, `red-p12-fixed.*` ran the P12 parent again with production still equal to 87. The interceptor now matches the exact canonical named-container inspection command and configured target, independently decodes mount values, preserves non-mount JSON and command metadata, and returns unrelated responses unchanged. All three cases failed at the intended comparisons, with no effects:

| Case | Stage reads | Predecessor final reads |
| --- | --- | --- |
| First/second stage order | 2 | 8 |
| Final third stage order | 3 | 12 |
| Nested predecessor order across outer reads | 2 | 8 |

The corrected P12 child exited 1; package time was 100.599 seconds and wrapper wall time 109.938 seconds. The original P7 result was retained without repeating it for the P12 fixture correction.

| Raw artifact | SHA256 |
| --- | --- |
| `red.jsonl` | `79dd363db3cd3096817449a86112990d9b22446edd8ba6d6b5b3b2b0406a7bc8` |
| `red-p12-fixed.jsonl` | `d9a988371569e637107cecaf0b486ece74e36b17f8e9665a08b70aa9ab8880f1` |

## Frozen verification

All artifacts below are under `temp/m3-owned-mount-stability-20261010/`. `green.command.json` and `final.command.json` retain complete executable argument arrays, process environment, exact parent/subcase inventories and input hashes. Verification used process-local official Go 1.26.7 with readonly module flags; no global toolchain configuration or module files changed.

Native Windows selected these seven parents with an anchored alternation, `-count=1 -json -timeout=20m`:

```text
TestGatewayRebindTypedStageCopyStageConfigAcceptsOnlyOwnedMountOrderChange
TestGatewayRebindTypedStageCopyStageConfigRejectsMountAndCommandDrift
TestGatewayRebindTypedHandoverPhysicalComparatorPreservesExactFields
TestGatewayRebindTypedPrepareHandoverAcceptsOnlyOwnedMountOrderChanges
TestStableGatewayV2ContainerMountsAcceptsOnlyExactMountOrderChange
TestGatewayRebindTypedStageRuntimeCommandsStartReplayAndCopyFinalConfig
TestGatewayRebindTypedHandoverRuntimeReconcilesActualFinalCreate
```

Actual WSL selected the same parents plus `TestGatewayRebindTypedHandoverRuntimeRollsBackStageRemovalBeforeFinalCreate`, exercising the changed rollback-prefix path through the coordinator's durable ABORT and exact cleanup assertions. Windows Go cross-compiled the Linux binary; the binary then executed inside WSL with the generatedingress package as its working directory. This is actual Linux execution, not compile-only evidence.

| Gate | Observed result | Receipt |
| --- | --- | --- |
| Native Windows | Exit 0; exactly 7 parents and 17 leaf subcases passed; no failure or skip; package 216.145 seconds, wall 223.813 seconds | `green.result.json`, `green.exit.json` |
| Linux cross-compile | Exit 0, 54.000 seconds | `final.cross_compile.exit.json` |
| Actual WSL | Exit 0; exactly 8 parents and 17 leaf subcases passed; no failure, panic or skip; wall 25.375 seconds | `final.actual_wsl.exit.json`, `final.actual_wsl.log` |
| Changed-file `gofmt -d` | Exit 0, empty output | `final.gofmt.exit.json` |
| `git diff --check` | Exit 0; only configured line-ending conversion notices | `final.diffcheck.exit.json` |
| `go vet ./internal/generatedingress ./cmd/hostd` | Exit 0, empty output, 58.954 seconds | `final.vet.exit.json` |
| Four CLI production build | Exit 0, empty output, 47.093 seconds | `final.build.exit.json` |

The production build command was:

```text
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
```

The final wrapper's terminal exit 0 was directly collected from retained session 83231, chunk `5d483c`. Each child exit was also persisted before output assessment. Every step's pre/post 795-file Go/module manifest matched the frozen input manifest. Native and WSL inventories and output hashes were independently checked in `qa-native-assessment.json` and `qa-wsl-assessment.json`.

| Artifact | SHA256 |
| --- | --- |
| `source-freeze-pre-green-v2.json` | `C8DC5A5CD386DCD85096175CFA068234E1395CDE8F2DB34371CC9602D2EE0D5C` |
| `green.jsonl` | `381424b8fc3b4c8cab45c75af0d6b4eecd4d819c390dad90c378849f9ecd546d` |
| `final.actual_wsl.log` | `0A3FFD155B5D38780DBBE7B59754087F5F9D3B57A2AC7222002668E295CD0A59` |
| `final.result.json` | `C631AF6D024EFE7EE20C295C232BAAFEC4999F23E306CCFE081D97C8C0B3F528` |
| Linux test binary | `4FA5B54D506FB138ABFA02C61E6F423CB19BCC730C9FA0FD2B3E05A760DC20DD` |

## Comparison and ownership controls

The P7 runtime control now accepts independently decoded exact mounts in reversed order, performs exactly one copy and two archives, and retains four stage inspections. Changed, added, removed or duplicate mounts and command-order changes still refuse before archive/copy/effects. The three P12 runtime controls now succeed with exactly three stage and twelve predecessor final inspections and no effects.

The separate physical-comparator control verifies fresh slices for stage, final and nested predecessor mounts, a fresh nested observation pointer, and unchanged caller-owned inputs. It rejects invalid/duplicate successor and predecessor ownership, presence mismatches, absent-container raw mount differences and unrelated command-order drift. Its synthesized successor-final value tests the comparator contract; the retained final-create runtime parent separately exercises the real concrete final-container path.

Security and code-review dispositions are retained in `source-reviews.md`. Both returned source GO, with no ownership or authority weakening found. The code review was manual; CodeRabbit was unavailable. `qa-ci-selection.json` confirms all four new parents map exactly once to `rebind-typed-runtime` in the existing required Linux and Windows matrices. CI workflows and the shared selector are unchanged.

## Documentation and final disposition

Root-owned documentation commands run after this page freezes:

```text
pnpm --dir docs check:workflow
pnpm --dir docs build
pnpm --dir docs check:accessibility
```

Their observed outcome is recorded in `temp/m3-owned-mount-stability-docs-check-normal-user-20261010/result.json`, without rewriting this page to include its own check result. Final-integrator disposition and any local commit identity are retained alongside the unit's external evidence. Neither source review nor documentation success authorizes publication.

## Limits

No local native Docker daemon is available for this unit. The two actual typed Docker journeys remain required after a separately authorized publication. Unit and simulated-runner success cannot substitute for actual Docker, a killed full controller, the complete durable-write/effect crash matrix, second-device LAN proof, or factory activation acceptance.

The unrelated Windows Node TLS failure and repository package capacity failure are outside this repair. No timeout, CI, module, schema, deployment, or public activation change is included. The production rebind factory gate remains closed. Rollback of this focused source change is a normal revert; it introduces no persisted data format or migration.
