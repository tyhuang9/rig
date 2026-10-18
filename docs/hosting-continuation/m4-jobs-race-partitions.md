# Jobs race-test partitions

Local contract and static verification passed on 2026-10-18. Actual hosted lane execution remains required.

## Hosted failure

PR #189 at `3160faa51a01e0691d093f6e46c07dfd50356006` exhausted the existing 40-minute Go test timeout in the jobs package. The retained job is [Repository heavy race tests (jobs)](https://github.com/tyhuang9/rig/actions/runs/39170132413/job/117801274452). Its log SHA-256 is `ed8bc137ea2350a95c13df1bb36c804969e19a4430a2458c50ad5559f6e809a0`.

Independent analysis found 82 source parents: 75 completed successfully, one had started, and six had not started. The 75 completed parent durations sum to 2,381.74 seconds. The package timed out at 2,400.023 seconds while the next case had been running for roughly 18 seconds in a fresh SQLite database migration. The log contains no assertion failure or data-race warning. This supports cumulative workload as the cause; it does not prove any unrun case passes.

## Narrow repair

Split only the jobs race lane into four disjoint selectors. Keep race detection, count 1, the 40-minute package timeout, the 80-minute workflow job limit, production/tests and aggregate required gates unchanged. The repository remainder still excludes the separately assigned jobs package.

| Lane | Current parents | Passing time observed in failed job |
| --- | ---: | ---: |
| Atomic worker races | 2 | 639.11 seconds |
| Scoped/admitted worker contracts | 10 | 240.07 seconds |
| Compose worker journeys | 10 | 102.68 seconds; three incomplete |
| Exhaustive remainder | 60 | 1,399.88 seconds; four unrun |

Three positive prefixes select the first lanes. The final lane skips their union, retaining future unmatched Test, Example and Fuzz parents. Tests verify the exact selector contract and failure behavior, and the compiled parent inventory is checked for complete unique assignment. No parent or subcase is removed, and the 16 iterations in each expensive atomic race remain intact.

The remainder's four unrun parents perform six fresh database setups. At the observed runner pace, approximately 25 minutes total is plausible; this is an estimate, not completed acceptance. Hosted execution must establish every lane's actual duration and outcome.

## Verification

- Before and after the change, `bash scripts/check-repository-race_test.sh` passed under WSL using task-local, checksum-verified jq 1.7.1. These are fake-Go contract tests: they verify orchestration and failure propagation, not actual Go race behavior. Temporary execution copies normalized Windows line endings; repository content was not modified by execution.
- Pinned Go 1.26.7 `go test -list . ./internal/jobs` discovered 82 unique parents. The actual inventory was audited against the script's three positive selectors and their derived catch-all union: 2 + 10 + 10 + 60, with no overlap or omission.
- Bash syntax, workflow YAML/matrix/80-minute limit and `git diff --check` passed. Independent source review confirmed the existing aggregate gates and `fail-fast: false` remain intact.
- Existing workflow changes run the same shell contract locally and in CI. No production Go code or Go tests changed. The monolithic 40-minute race suite was not rerun locally; hosted execution must supply actual timings and all results.

Retained evidence lives under `temp/m3-lan-external-db-20261015/m4-jobs-race-partitions-verification/` in the implementation workspace:

| Artifact | SHA-256 |
| --- | --- |
| `baseline-contract-receipt.json` | `2947d1d6d9a81eb94cbde26b9cdfd9b51f38e3910dd5673d0b1debf57083f762` |
| `final-contract-receipt.json` | `c9f4fe4fa6adfd15bba1d21a1ad75dd37aec199eace05f1a67cbbcc543d011fe` |
| `final-source-freeze-sha256.json` | `da55f9fe78ea6efa29cba3dd1acbf147ad0587728749329756280c3cc27c48d6` |
| `jobs-discovery.txt` | `3b35625f253d947c5a6d139c794d92eb3802efc554c1e7b730794c25b8aef19d` |

The combined root audit is `m4-lifecycle-coordinator-verification/live-ci-repair-evidence-audit.json`, SHA-256 `6510c0fe7fb767d40ca84c4d9e8a679dae62b21e2184a0295e5a19389cc2ae44`. It verifies the source freeze, contract receipts, compiled inventory assignment, and the separately reviewed live-caller repair artifacts.

This CI-only change does not alter controller behavior, runtime authority, data, secrets or database ownership. No managed database or Neon provisioning is added. It does not establish Docker acceptance or finish M4. If the split must be rolled back, preserve a required jobs race lane covering the full package; do not remove the coverage merely to make CI pass.
