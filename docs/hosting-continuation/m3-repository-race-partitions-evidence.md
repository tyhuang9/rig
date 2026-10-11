# M3 repository race CI capacity

Status: selector contracts, complete inventory audit and independent source reviews passed. Documentation and final integration results are retained separately after this page is frozen. Hosted capacity acceptance remains pending; this change does not close M3 acceptance.

## Scope and source

The isolated branch `feature/hosting-m3-repository-race-partitions` starts at published source `933bc81ff65cdd98252abfa0a83cdc5ebf768475`. Its purpose is to partition the existing non-ingress repository race invocation while preserving complete coverage and failure propagation.

The separate mount-order repair `457538a` was published to draft PR #145 after explicit approval. It is not incorporated into this branch. No production Go, fixture, module, schema, runtime authority, history, secret handling or database ownership change is part of the CI repair.

## Observed hosted failure

PR #145 source `933bc81` was tested as merge `5e30c4ed7a78004f1106ebff695d89579b33597f`, whose tree matched that source. In [GitHub deployment CI run 38107833383](https://github.com/tyhuang9/rig/actions/runs/38107833383), job `114376996331` passed the dedicated PostgreSQL integration and relay outage race checks. Its subsequent repository race invocation failed:

- The controller package reached its existing 40-minute limit, reporting package duration 2,400.058 seconds.
- The named active test, `TestLocalReleaseHistoryNeverExposesAbsoluteSourceOrWorkspacePath`, was reported as zero seconds old. Its goroutine was runnable in SQLite schema preparation through `databasefixture.Open` and production `database.Open`.
- Other packages continued after that package failure. The job reached its existing 80-minute limit; runner cleanup terminated the still-running `jobs.test` process.
- The always-run repository aggregate failed. The workflow concluded cancelled, not successful.

The log establishes cumulative package and job limits. It does not establish a deadlock or a defect in the test that had just started. The fixture's production database reopen retains real pragmas and migration-ledger checks and is left intact.

### Same-source timing comparison

The fast repository job `114376996462` passed on the same tested merge. Selected complete-package durations were:

| Package | Nonrace seconds | Race seconds |
| --- | ---: | ---: |
| `cmd/hostd` | 50.530 | 1,904.391 |
| `internal/appaccess` | 51.716 | 1,508.932 |
| `internal/appconfig` | 23.563 | 917.882 |
| `internal/controller` | 92.766 | 2,400.058, timeout |
| `internal/database` | 29.130 | 1,237.307 |
| `internal/generatedrecovery` | 49.396 | 1,958.833 |
| `internal/jobs` | 76.100 | No terminal package result |

These are separate runners with different instrumentation and workloads. The ratios do not isolate instrumentation overhead or prove which concurrent package combination caused the slowdown. The second, 0.018-second controller result in the fast log belongs to a separate contract check and is not its whole-package duration.

Raw decoded connector responses, exact timing lines and the terminal assessment are retained under `temp/pr145-933-hosted-acceptance-20261010/`:

| Artifact | SHA256 |
| --- | --- |
| `job-114376996331-response.json` | `8C7C30E99162A3F7BA74CEB14175036871E8A9C9F0D8BDE48CF06CAEE565A96A` |
| `job-114376996462-response.json` | `8D0001774D25EFAAD1BE41F6502C631565E272AFF5AFF756C2D685F6740D8187` |

`repository-timing-comparison.json` retains the matching log lines. These hashes cover serialized connector-decoded responses, not original GitHub archive bytes.

## Coverage design

The existing PostgreSQL job keeps its service and both database-specific steps, then executes the dynamic repository remainder. Eight new matrix lanes cover `cmd/hostd`, `internal/appaccess`, `internal/appconfig`, `internal/database`, `internal/generatedrecovery`, `internal/jobs`, and the two complementary controller selections.

Controller lifecycle selection uses `^Test(Deployment|Runtime|Latest|Reviewed|Prior|Release|Secret|Local|Resume|Generated)`. The controller remainder excludes only that prefix, retaining other tests, examples and fuzz seeds. Complete descendant test trees remain selected. Future controller names enter one of these complementary groups; future packages enter the dynamic repository remainder.

The shared `scripts/check-repository-race.sh` keeps `-race`, `-count=1`, the 40-minute test-binary limit and 80-minute job limit. Generated ingress remains covered by its existing separate mandatory partitions. The existing always-run aggregate now requires PostgreSQL/remainder, every new matrix lane and ingress. Fast verification runs the selector contract check.

## Clean baseline inventory

On clean exact source `933bc81`, Go 1.26.7 discovered 51 Linux packages. A Linux controller test binary was cross-compiled on Windows and executed in WSL with list-only selection `^(Test|Example|Fuzz)`. Its 116 runnable parents partitioned into 26 lifecycle parents and 90 remainder parents, with no overlap or omission. These are coverage counts, not a timing-balance claim or behavioral test pass.

All commands exited zero. Package discovery took 2.797 seconds; Linux compilation 24.546 seconds; actual WSL listing 4.187 seconds. All 795 Go/module input hashes remained unchanged. The retained wrapper session `51530` finished with exit zero.

Receipt: `temp/m3-repository-race-partitions-20261010/baseline.result.json`, SHA256 `BE694F7B4DB81D98416EA4B22FB1A611EB0CF1D205E6D99EE4E75E455D7E36B5`. Compiled binary SHA256: `ADFEC1C2B11755E576847F45EAACDF357D6535DB281D54445DADA42FAA0D6A5C`.

## Selector verification

Actual WSL Bash executed the checked-in `scripts/check-repository-race_test.sh` with real `jq` and synthetic `go`/`tee` commands. It passed with exit zero in 5.564 seconds. This exercises the selector's command construction and failure behavior; it does not execute the repository's production test suites or race detector.

The contracts check all nine selected command lines, preserving race flags, deadlines and complete package selection. They accept future-package fuzz parents and a bare `Example` parent, and reject unknown or missing arguments, duplicate or missing packages, malformed inventories, an empty remainder, `TestMain`-only or child-only events, and missing package completion. Discovery, test and log-capture failures produce otherwise valid output before returning nonzero, proving that output cannot mask their failure.

The final inventory audit confirms 51 packages: 43 in the dynamic remainder, six fixed heavy packages, controller in its two complementary groups, and ingress in its existing groups. All 116 compiled controller parents are covered exactly once. The PostgreSQL service and database-specific steps, plus the complete ingress block, are byte-identical to source `933bc81`. The matrix, aggregate dependency/results checks, pinned actions and diff check pass. All 795 Go/module hashes remain unchanged.

Manual code/QA and security/coverage reviews are GO on the exact three source hashes below. CodeRabbit was unavailable; no CodeRabbit review or production-suite rerun is claimed. The root reviewed the final workflow and scripts; aggregate final review follows the frozen documentation check.

Artifacts are retained under `temp/m3-repository-race-partitions-20261010/`:

| Artifact | SHA256 |
| --- | --- |
| `selector-contract-jq-argv.result.json` | `DC150DE14E07BFCB33D1EFCD4894ABB263E50A3197C30A12A3AA1EF7584CC888` |
| `selector-contract-jq-argv.output.log` | `014D3F0A6872EB7873BCE1FB97BFC8AD2651E2ADFB3E750BA91BCB2BE2F8865C` |
| `selector-final-audit.json` | `7E4334F10192F951D6FFF81B97067203A9EACFE6677DD6263ABAE3EF084A57D7` |
| `scripts/check-repository-race.sh` | `D4E70719806C87AB605A1C3E7C173B94985B66F00CE434A5F050F071BF054FA2` |
| `scripts/check-repository-race_test.sh` | `6C6472CC6F0C193AFE622B145B04CB80DF5FBA3627289195E97D5F428FC55F7F` |
| `.github/workflows/github-deployment-ci.yml` | `6E6BA78F8144344BC54DBA49533205CF8F336264327BE1532AC5A9BFC35F3562` |

### Retained tooling failures

The initial selector syntax check passed, but contract execution stopped with exit 127 because WSL lacked `jq`. Its `selector-contract-final.*` receipts remain intact. A subsequent launcher failed before contract execution because its inherited PATH contained unquoted spaces; `selector-contract-jq-1.8.*` retains that attempt. The corrected launcher uses argument arrays and a fixed process-local PATH. No repository source changed between these attempts.

Verification used an isolated official [jq 1.8.1 Linux binary](https://github.com/jqlang/jq/releases/tag/jq-1.8.1), with SHA256 `020468DE7539CE70EF1BCEAF7CDE2E8C4F2CA6C3AFB84642AABC5C97D9FC2A0D` matching the published release asset digest. `tools/jq-1.8.1/provenance.json` records the source and comparison. No global installation or environment change was needed. The syntax command parses the selector; successful contract execution also establishes the contract script's syntax.

## Documentation and remaining acceptance

Frozen offline documentation installation passed with unchanged inputs. After this page freezes, run `pnpm --dir docs check:workflow`, `build` and `check:accessibility`; their outcomes and final review are retained separately to avoid editing the checked inputs solely to report their own results. No new-head hosted race result is claimed.

After separately approved publication, acceptance requires the actual tested source identity, successful PostgreSQL checks, all nine non-ingress race lanes, all ingress partitions and the aggregate. Partitioning adds runner jobs; it does not establish sufficient hosted capacity until those jobs finish. Existing runtime and Docker acceptance requirements remain separate.

Rollback is an ordinary reviewed revert of the CI selection change. There is no persisted data-format change or migration. Publication, merge and deployment require their existing explicit approvals.
