# M3 stage configuration projection repair

## Scope and invariant

Follow-up to published PR137 at
`f49cf793bfae06f3d28fa45d4609a9489075cccb`, targeting PR136's branch.
Recompute the stage container's original create configuration after final
configuration progress has been appended. Keep its immutable container,
image, network, volume and ownership contract, and all protected history.

The configuration digest helper already projects away post-create progress.
It cleared `StageServing` but retained the later `FinalConfigIntent` and
`FinalConfigCopy` pointers. Valid sequence-11/12 state therefore became an
invalid intermediate projection: the final intent requires a serving receipt.
The repair clears those two pointers on the helper's local value copy before
reconstructing the original creation arguments. It changes no stored bytes,
schema, authority checks, Docker commands, runtime deadlines or factory setting.

The new regression builds contiguous protected progress through sequence 12
using the existing repository fixture. It compares the sequence-10/11/12 digest
with both the pre-create sequence-five digest and retained container binding,
requires classification of the exact serving stage, and checks that input and
protected file bytes remain unchanged. A changed image and replacement binding
remain rejected. The digest projection does not grant authority; complete
progress and handover context validation remain required by their callers.

External databases remain application-owned and configured through scoped
runtime secrets. This change adds no managed database provisioning.

## Reproduced failure and local verification

Toolchain: Go 1.27.0 on Windows; hosted Go remains pinned to 1.26.7. Commands
used `GOTOOLCHAIN=local` and `GOFLAGS=-mod=readonly -buildvcs=false` (with `-p=1`
for platform/build checks). Each result records the source hashes before and
after execution. No tested source changed during any successful check.

1. The first sandboxed regression failed before its assertions because the
   existing protected builder directory fixture received `Access is denied`.
   It is preserved as an environment failure, not the reproduced defect.
2. Against unchanged f49 production code, the native regression passed sequence
   10 but failed sequences 11 and 12 on digest and stage-runtime classification.
   Exit 1, package 13.622 seconds. The changed-image digest control also failed
   because the invalid projection prevented digest calculation.
3. After the two-line repair, the regression passed one parent and five
   subcases, with no failures or skips: exit 0, package 13.787 seconds.
4. Four existing adjacent parents and 15 subcases passed, with no failures or
   skips: exit 0, package 42.400 seconds. They retain generation-scoped private
   create arguments, physical-drift replay refusal, rejection of later-phase
   history by stage start, and exact handover resources/volume users.
5. A Linux test binary was cross-compiled with `GOOS=linux GOARCH=amd64
   CGO_ENABLED=0`, then actually executed in WSL from the package directory.
   The new parent and all five subcases passed, no skip/failure (1.797 seconds
   wall time). This is plain Linux execution, not a Linux race run.
6. `go vet ./internal/generatedingress` and
   `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe`
   passed. Independent security review found no blocker in the two-file patch.

Exact test selections:

```text
go test ./internal/generatedingress -run '^TestGatewayRebindStageConfigurationDigestPreservesFinalProgress$' -count=1 -json -timeout=15m
go test ./internal/generatedingress -run '^TestGatewayRebind(StageContainerCreateArgsAreGenerationScopedStoppedAndPrivate|StageContainerReplayRejectsPhysicalDriftWithoutMutation|StageStartStillRejectsLaterPhaseHistory|FinalHandoverDriverRequiresExactResourcesAndVolumeUsers)$' -count=1 -json -timeout=15m
```

The new parent matches the existing `rebind-config` partition exactly once:
`StageConfiguration` matches `StageConfig`; the resource and fallback groups
exclude that config family. No CI filters or timeouts changed.

Local records are in `temp/pr137-stage-projection-20261009/`:

Verified production file SHA-256:
`34F107ADBF7A5096F023C8282F9C7132D357D23411581D1240A867A9B9D3551E`.
Verified regression file SHA-256:
`5FC3D0182E2E872F28F91460E7E40375840937C48F2A02F4EE7032B29D556A18`.

| Output | SHA-256 |
| --- | --- |
| Sandbox environment failure | `7668AEFA2426E348B994838A30419DE92EFBF7CC013EFE859076DF8345E76D56` |
| Native reproduced failure | `B0F428B06725CFBA5AEC6B206CEA4E0E5FF8E5C9CDA3DC6C576C1382E650D89A` |
| Repaired native regression | `E503BAC1EB02C56F89DAFCF779311D050DF31D0FCA95B8BFC2E250839C2042C8` |
| Adjacent native regressions | `95CA7BAA4D83A2B05C12EFD44DEBA466A86474DAFDA0069F6B8AADBA71E18C9B` |
| Actual WSL regression | `04AA8E20A09B59BDEABC9D870F8405476EC439615FA03124ED2152405A74321F` |

## Published f49 hosted results

These results establish acceptance at f49 only, before this repair:

| Workflow / run | Result |
| --- | --- |
| [Documentation / 37966458479](https://github.com/tyhuang9/rig/actions/runs/37966458479) | Success; documentation deployment skipped |
| [Browser / 37966458571](https://github.com/tyhuang9/rig/actions/runs/37966458571) | Four browser tests passed |
| [Two-app LAN / 37966458532](https://github.com/tyhuang9/rig/actions/runs/37966458532) | Live journey passed in 222.68 seconds; residue check passed |
| [Migration / 37966458434](https://github.com/tyhuang9/rig/actions/runs/37966458434) | Immutable-source and live migration/uncertainty parents passed; residue check passed |
| [Relay Compose / 37966458446](https://github.com/tyhuang9/rig/actions/runs/37966458446) | Success |
| [Controller / 37966458587](https://github.com/tyhuang9/rig/actions/runs/37966458587) | Both jobs succeeded |
| [Generated runtime lifecycle / 37966458473](https://github.com/tyhuang9/rig/actions/runs/37966458473) | All 21 jobs succeeded, including the full 18-group ingress race matrix and strict aggregate |
| [Windows / 37966458417](https://github.com/tyhuang9/rig/actions/runs/37966458417) | All 20 jobs succeeded |
| [Gateway Docker / 37966458420](https://github.com/tyhuang9/rig/actions/runs/37966458420) | 11 jobs passed; four rebind journeys failed |
| [GitHub deployment / 37966458523](https://github.com/tyhuang9/rig/actions/runs/37966458523) | 20 jobs passed, including its full ingress race matrix; repository job cancelled and strict aggregate failed |

Both private handover children reported `stage_runtime` at
`final_config_copied`, with zero handover effects. This motivated the regression.
Typed commit instead produced a rolled-back/abort result; typed rollback refused
before its intended sequence-17 boundary. Their physical failures are not yet
proved repaired by this local change.

The repository job passed PostgreSQL store integration and the strict relay
outage convergence step. Its later complete repository race command timed out
at 40 minutes in appaccess, autodeploy, controller and controllerrelay. The
generatedimage symlink test also failed its five-second startup probe. The job
was cancelled at approximately its configured 80-minute limit, and the strict
aggregate failed. Timeout causation and the static-server startup failure remain
open. A passed earlier step does not make the entire repository race gate pass.

All original failed attempts remain preserved. No workflow was rerun or
cancelled by the agent. Passing LAN/Windows results do not establish why their
earlier attempts stalled. Physical second-device LAN/NIC drift acceptance is
separate from these hosted fixtures.

All final run metadata and failed job logs are retained under
`temp/pr137-f49-ci-20261009/`; `acceptance-log-manifest.json` records 23 additional
successful job logs (SHA-256
`86AC83CA32AB82AAC5F676F242D97D94EA3A5C9D8B1AAC8796BB2EC46DAEC70F`).
The preserved matrix log sample is bounded; the complete success statements
above refer to the final job metadata and strict aggregates, not a newly counted
per-test inventory.

## Remaining acceptance and rollback

Actual Docker execution is unavailable locally. Hosted private and typed
commit/rollback journeys must still verify the repaired source, with strict
test discovery and cleanup. Documentation check and final integration review
results are recorded separately with this follow-up's local delivery record.
M3 remains incomplete; production
factories stay closed. This local follow-up is not yet published or merged.

Reverting the two-line projection repair and its regression restores the prior
refusal without changing stored state. Preserve all protected progress, SQL
history and migration records; do not rewrite history to force acceptance.
The independent transferred-lifecycle branch is outside this follow-up.
