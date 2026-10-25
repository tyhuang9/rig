# M4 complementary race-test partitions

Status: local selector verification passed on
`feature/hosting-m4-complementary-race-budgets`, based on PR #223 at `bbf6a0f`.
Hosted execution of these partitions remains pending.

## Observed limit

PR #223's repository race job reached the unchanged 40-minute package limit in
three packages. Before each timeout, the log recorded many passing outer parents
and no failing outer parent. The unfinished tests were initializing database
fixtures, with runnable SQLite migration frames in the timeout snapshots.

| Package | Parents started / passed | Completed-parent time | Package timeout |
| --- | --- | --- | --- |
| `deploymentexecution` | 29 / 28 | 2,371.52 seconds | 2,400.072 seconds |
| `generatedruntime` | 59 / 58 | 2,375.13 seconds | 2,400.050 seconds |
| `generatedruntimestate` | 21 / 20 | 2,392.72 seconds | 2,400.040 seconds |

These are incomplete execution prefixes, not complete package inventories. The
unfinished parents had run for approximately 28.5, 24.9 and 7.3 seconds. The log
supports cumulative package-budget exhaustion; it does not establish an individual
test deadlock or explain every source of runtime cost.

The separate ingress `rebind-history` suite reached its 32-minute limit after 160
passing parents and 1,913.71 seconds of completed-parent time, with its next parent
unfinished. That is the fourth partition addressed here.

## Partition contract

Each repository package gains two suites: an exact anchored set of known costly
parents and its exact complement. Those packages are explicitly excluded from the
repository remainder. The existing ingress history suite similarly gains a heavy
suite and excludes that exact set from its original selection.

| New heavy suite | Selected parents | Observed completed-parent time |
| --- | --- | --- |
| `deploymentexecution-heavy` | 6 | 1,425.55 seconds |
| `generatedruntime-heavy` | 9 | 903.18 seconds |
| `generatedruntimestate-heavy` | 5 | 1,728.22 seconds |
| `rebind-history-heavy` | 7 | 770.62 seconds |

These timings guide grouping. They are not predictions or acceptance evidence for
the unexecuted remainder. Every new parent must still belong to exactly one suite.

The change is limited to four runner/contract scripts and three workflow matrices.
It preserves test bodies, fixture helpers, race and plain modes, isolation policy,
test deadlines, step and job deadlines, and production behavior.

## Local verification

Fresh Windows binaries compiled successfully on October 24, 2026. Actual parent
discovery produced these inventories, which supersede the planning estimates:

| Package or family | Heavy parents | Remaining parents | Total |
| --- | --- | --- | --- |
| `deploymentexecution` | 6 | 26 | 32 |
| `generatedruntime` | 9 | 54 | 63 |
| `generatedruntimestate` | 5 | 17 | 22 |
| Ingress rebind history | 7 | 158 | 165 |

Forty-one actual runner invocations captured include/skip arguments through an
explicit no-op Go shim. Applying those arguments to the compiled inventories
proved nonempty, disjoint, exhaustive selections for the three packages and all
1,232 ingress parents across 34 suites. The repository remainder excludes all
three newly designated packages. All three workflow matrices matched their suite
sets. This checks dispatch and coverage; simulated PASS events are not behavioral
test results.

Both shell contract scripts passed with pinned real jq 1.8.1 on October 25, with
native raw logs retained. Their negative controls cover failed commands, empty or
child-only selection, missing package PASS, and failed output capture. The
canonical `pwsh -NoProfile -File scripts/check-generation.ps1` check passed on
October 24. The seven CI source files remained unchanged through verification;
the checkout has no Go, module, or lockfile changes.

Earlier PATH/CRLF harness failures and an incorrect intermediate selector
evaluation remain preserved. The final root audit recomputed selections directly
from captured arguments. The canonical receipt hashes LF text while its saved
stdout uses CRLF; both hashes are recorded without rewriting the original log.

Hosted jobs must still show all selected outer-parent and package PASS results
under the existing race and timeout settings. Local Windows discovery does not
establish the Linux inventory or application behavior.

## Source evidence

- [Repository race job](https://github.com/tyhuang9/rig/actions/runs/40074402026/job/120814568234),
  raw SHA-256 `21e3cf174a2d109587aac2438a69b293ae436033d363f31ad80c1a9fa3c97348`.
- Repository timing census SHA-256
  `5bbd6ab1a0b13a59e4960fda8b159ffb19d444e25e553d48259ccf6bb8e5edfc`.
- Ingress history timing census SHA-256
  `0d41cee44f88dd2b8749756539131e03b310a4d4f853edb53f56880712984bb2`.
- Final local root audit SHA-256
  `b1ad5c995000c070dc46606570f186fab5c082e56cf2355bb51e54d6be6351ae`.

The hosted merge `3cc3514` and PR head `bbf6a0f` share tree `e66dcb0`. Partial
timeout inventories therefore do not indicate different source trees.

## Rollback and limits

Reverting these runner and workflow changes restores the previous scheduling and
its observed timeout risk. No runtime state or application data changes. Passing
partition checks cannot establish Docker behavior, resolve independent runtime
failures, or complete M4 acceptance.
