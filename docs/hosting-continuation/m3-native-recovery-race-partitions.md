# M3 native recovery race partitions

## Observed capacity failure

[Repository native-composition job 116494043326](https://github.com/tyhuang9/rig/actions/runs/38755433461/job/116494043326)
at `2a5812f21a81d085288ffd702903c835995bb04d` exhausted the unchanged
32-minute package limit. Actual checkout
`6c7cebc28f7fc40fabb99d6713c4db704e55fb03` matched the published tree.
Raw log SHA-256:
`3324683456ef1ccf577ffc8cb1ba483d992f5d94f8b31116907fc9134a44762e`.

Five complete parents passed in 1882.77 seconds. Retained rollback had run only
37 seconds when the package alarm fired; transferred lifecycle had not started.
The stack shows a runnable digest computation. It does not establish a digest
defect, deadlock, or race failure.

## Partition change

| Group | Complete parents | Planning evidence |
| --- | ---: | --- |
| `rebind-composition-startup` | 3 | Both fresh-A0 reconstructions plus unbound-create adoption; 1009.77s completed in the failing job |
| `rebind-composition-transferred` | 1 | Transferred LAN lifecycle; 596.91s in the older-source run below |
| `rebind-composition-native` | 3 | Commit/replay, durable-failure recovery, retained rollback; first two took 873.00s in the failing job |

[Older-source native race job 116446182928](https://github.com/tyhuang9/rig/actions/runs/38742111063/job/116446182928)
at `a20db996dbf2cd32845168422aa7a855fe0a0c3e` passed its four native parents
in 1679.061 seconds, including retained rollback in 241.25s and transferred
lifecycle in 596.91s. Raw SHA-256:
`a9eea91dce44c75d6779913c45338a29238a6a87909085a23fbb84714f29a5d8`.
These different-source timings inform grouping; their sum is not a prediction
or acceptance result for this branch.

The shared selector moves complete startup/adoption and transferred families
into the two new groups. Native remains the fallback for unmatched composition
parents. Existing second-generation and legacy-source groups are unchanged.
All three workflows include the same five composition groups.

No test body, runtime behavior, parent or descendant assertion is changed.
Linux retains `-race`; Windows retains `plain`; all retain `-count=1`, the
32-minute package limit, 34-minute step limit, 42-minute partition-job limit,
fail-fast settings and aggregate dependencies. PR #161's running Docker matrix
continues on its existing revision.

## Executed verification

- Compiled actual Windows and Linux test inventories: **1055** and **1057**
  parents respectively. Go-regex evaluation of every shared selector assigns
  each parent exactly once, with no empty group. Old seven-parent native coverage
  equals the new **3 + 3 + 1** union; every other group's members are unchanged.
- Seven synthetic future names retain one owner, including new startup,
  transferred, native fallback, second-generation and legacy families.
- `bash scripts/check-generated-runtime-race_test.sh` passes using real jq 1.8.1
  and isolated Go/tee stubs: 15 exact invocations in default/race/plain modes,
  three equal workflow composition matrices, and eight refusal/failure cases.
  Empty/child-only results and Go/tee failures cannot satisfy the gate.
- Missing and duplicate workflow-row mutation controls fail the same contract.
- Bash syntax and canonical `scripts/check-generation.ps1` pass. Native Git Bash
  has no jq, so complete native shell-contract execution is not claimed; the
  Linux contract exercises both race and Windows-plain argument construction.
  Initial WSL syntax inspection rejected the Windows checkout's CRLF shell file;
  the modified shell files use LF.

The contract check runs beside the repository-selector check in Fast
verification. Inventory and synthetic checks establish selection and failure
propagation, not actual race execution or runtime capacity. No local Docker or
race toolchain is available. Hosted completion of all new partitions and the
remaining M3 gates is still required. The separate Windows Node startup failure
is not repaired here. Reverting this CI-only change restores the former grouping
without changing application behavior or stored data.

Exact commands, source hashes and outputs are retained in wrapper
`temp/m3-lan-external-db-20261015/native-recovery-race-partitions-verification/`.
