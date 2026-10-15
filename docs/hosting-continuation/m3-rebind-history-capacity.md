# M3 rebind history CI capacity

Status: local selector checks and complete Windows test selections finished;
replacement hosted race and Windows acceptance remain pending. M3 remains open.
Base: PR #153 at `386bad5f62686e1f5a7bd549ef367ec0fc26dad8`.

## Observed failure and scope

The [Windows history job](https://github.com/tyhuang9/rig/actions/runs/38646046834/job/116133973983)
exhausted the existing 32-minute package deadline after passing 148 parent tests
and 420 subcases, with no preceding assertion failure. Completed parent elapsed
times totalled 1,871.78 seconds. The unfinished requested-recovery parent started
only about 48 seconds before the deadline. Log SHA-256:
`ce6d70739128701255896bb2c887f62389b2d9e79bef7011e337e0e4450c88c2`.

The [Linux race history job](https://github.com/tyhuang9/rig/actions/runs/38646046771/job/116133821344)
also reached 32 minutes after 100 parent and 288 subcase passes, without a prior
assertion failure or race warning. Completed parent times totalled 1,725.2 seconds.
Log SHA-256: `7d46ace549fc72d9337e5a5f1402042d9bcd07d0afc206a7d3a97a051b204f7e`.
These failed jobs remain failed; partitioning is not hosted acceptance.

Split the original history selection into four complementary groups:

- `rebind-startup-runtime`: complete Runtime and Startup families.
- `rebind-recovery-guards`: ForwardConfirmation, Interactive and RollbackConfirmation.
- `rebind-recovery`: Reconcile, Recovery and Requested families.
- `rebind-history`: the remaining original history parents.

All three workflow matrices include all four groups. Runtime and test bodies are
unchanged. Local timings and matched hosted Linux parents showed that a two-way
split still left inadequate race-run headroom. The matched Linux/local ratio
estimated the larger history group at about 31.6 minutes; moving startup/runtime
leaves about 23 minutes estimated. These are capacity estimates, not guarantees.

## Preserved gates

- Full parent/subtest trees and `-count=1`.
- Race detection by default; Windows retains its existing explicit plain mode.
- Package deadline 32 minutes, step deadline 34 minutes and job deadline 42 minutes.
- `pipefail`, required nonempty parent passes, matrix `fail-fast: false`, and all
  existing aggregate success dependencies.

## Executed selector verification

Evidence lives in `temp/m3-windows-history-capacity-20261014/` in the enclosing
workspace. Final runner SHA-256:
`a2b6f393a02aaed6fbf623aef96ba459c09aeceeb15cc40e219e9a9a364cc707`.

`verified-runner-controls.json` records syntax validation, 72 final actual Bash
argument captures across default/explicit race/plain modes, and whole-workflow
comparisons proving only three matrix rows were added. Four invalid-argument
cases failed. With pinned jq, parent-pass, empty selection, child-only selection,
Go failure and tee failure controls returned 0, 1, 1, 32 and 33 respectively.
Earlier controls remain retained separately; missing-jq attempts are not passes.

Compiled Windows/Linux inventories were audited using Go regular expressions
against captured baseline and final arguments (`*.verified-coverage.json`):

| Source | Platform | All ingress parents | History | Startup/runtime | Recovery | Guards |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| PR #153 `386bad5` | Windows | 1,038 | 155 | 24 | 8 | 5 |
| PR #153 `386bad5` | Linux | 1,040 | 155 | 24 | 8 | 5 |
| PR #155 `7a15861` | Windows | 1,043 | 160 | 24 | 8 | 5 |
| PR #155 `7a15861` | Linux | 1,045 | 160 | 24 | 8 | 5 |

Every discovered parent has exactly one owner. The four groups' disjoint union
equals old history. All unrelated selections are unchanged, all 23 ingress groups
are nonempty, and 14 synthetic future-family/fallback names retain one owner.
Source, compiled binaries and inventories are pinned in receipts.

## Actual test execution and platform limits

Two complete Windows selections ran against the unchanged compiled base binary,
with the existing 32-minute deadline. They cover the four final groups as complete
parent unions; the final four groups were not separately executed locally.

| Selection | Result | Duration | Log SHA-256 |
| --- | --- | ---: | --- |
| History plus startup/runtime | Exit 0; 179 parents ran, 178 passed, 581 subcases passed; one parent and three subcases skipped | 1,332.781 s | `5bc803066a6e0eb3fee10cfe67e99bb0da3ff54bdf7eae5220bb8d4920253190` |
| Recovery plus guards | Exit 0; all 13 parents and 58 subcases passed; no skips | 1,608.109 s | `3cc562f036d2a437d5e756daad2c1e4f2e9fb118c45a10fad8bd0970139a4744` |
| Linux platform cases | Exit 0; all three parents and five subcases passed; no skips | 5.031 s | `3ecbaa3142add5cc009e411eab40852a511d871d3067b677c46f032e570b7ba5` |

The Windows history receipt intentionally retains `passed: false`: its strict
recorder rejects any skip. The directory-permissions parent explicitly skips
Windows; three symlink cases lacked Windows symlink privilege. All affected
parents were then executed successfully on Linux. No assertion failed in either
complete Windows selection. Commands, exact discovery, unchanged source/binary
checks and raw logs are retained in `*.execution.result.json` and
`linux-platform-cases.result.json`. Local Linux checks did not use race detection.

## Limits and rollback

Hosted replacement groups must establish race safety and sufficient capacity;
local evidence does not establish either. Revert the shared selector and all three
matrix additions together to restore the previous grouping and its observed
capacity failure. No runtime safeguard, immutable history, authorization or
database behavior changes. Full supported-controller Docker recovery and physical
LAN acceptance remain separate M3 gates.
