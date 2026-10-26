# M4 cold-start test budgets

## Observed failures

The [PR #243 domain-construction race job](https://github.com/tyhuang9/rig/actions/runs/40271403301/job/121404806827)
at 5357344e57969df1effed8d10d797290aaf24da7 reported four 90-second
parent-process timeouts, then the 32-minute package timeout. It completed
35 parents with 1,890.25 seconds of accumulated test duration before the
package expired. The raw log contains no data-race warning. Its SHA-256 is
92de4d5848119221e82a3389ddb7bc84081130907267f9ebdc8b5070a0e53140.

The affected parents are ColdAcceptsCompleteRetainedV1UnderRebindOrigin,
ColdRejectsCompleteRetainedV1UnderRebindOrigin,
ColdRetainedGatewaySelectionTripleAndReadAttribution and
ColdRetainedGatewaySelectionAuthorityAndReaderAttribution, each with the
TestGeneratedDomain prefix. Each has three costly cases. Its outer registry
process shared one 90-second deadline across all three separately isolated
construction cases. Two of these parents also timed out in
[PR #242's race job](https://github.com/tyhuang9/rig/actions/runs/40268591644/job/121396283278).

## Changes and invariants

Move the registry-process guard inside each existing case, immediately before
the construction-process guard. Every case retains both process boundaries,
its existing name, body, assertions and cleanup. The four parent names and
12 case names stay unchanged. No registry reset or production change is added.

Split the former construction package selection into three complementary
groups. Keep complete parent trees and give existing cold prefixes priority:

| Group | Selection | Discovered Windows parents |
| --- | --- | ---: |
| domain-construction | Generated-domain tests excluding startup, the existing A0 group and all cold tests | 31 |
| domain-cold-retained | Cold tests containing Retained, excluding the existing cold group | 16 |
| domain-cold-remainder | Other cold tests outside both groups above | 6 |
| domain-cold | Existing prefix roster, unchanged | 19 |

The first three groups exactly cover the previous 53-parent construction
selection on the fresh Windows binary. The source-only planning count of
36 noncold parents is not an executable Windows count. Counts are evidence
for this revision; the new filters accept future family members.

The existing 90-second child, two-minute command-context, 32-minute package,
34-minute step and 42-minute job limits remain unchanged. Linux race detection,
Windows plain mode, output-capture failures, nonempty test discovery and all
aggregate gates remain enforced. All three workflows gain the same two groups,
adding six matrix jobs overall; this can increase queue time.

## Actual local verification

Based on b276f1eef074002e1e4424ba5c50fa828c0df1d3, a fresh Windows Go 1.26.7
binary discovered all four intended parents and passed all four with their
12 cases: 16 distinct RUN/PASS names, no failures or skips. The source uses
the same row assertions and process helpers as before. The toolchain was
pinned with GOTOOLCHAIN=local, GOFLAGS=-mod=readonly -p=1 -buildvcs=false
and CGO_ENABLED=0.

The shell contract passed with pinned jq 1.8.1 and a fake Go command that
captures arguments. It checks race/plain flags, future-family assignment,
existing-prefix overlap, all three workflow matrices, and refusal of empty,
child-only, failed-test and failed-output-capture results. It does not execute
Go behavior tests.

An independent audit evaluated actual captured runner arguments against all
1,292 parents from the fresh binary. Every parent belongs to exactly one of
36 ingress groups in both race and plain mode. The new construction groups
are nonempty, disjoint, and equal the former selection; the existing 19-parent
cold group remains disjoint. The audit receipt SHA-256 is
a7b57da63bb420a406465710ce39fe779eb8d4b8a86c3e09cee2044d8f7f8862.
The shell contract receipt SHA-256 is
63789ee2240231c3ed0d8764956151b46f8e1dd4c0dccf855ddf7ed29795ef47.

Formatting, ingress vet, canonical generation, diff checks and source-stability
checks passed. Hosted Linux race timing and full CI remain required. No local
Docker or broad race suite was run.

## Remaining acceptance and rollback

The independent A0 Docker run at b276f1e now identifies
cold_group_container_correspondence during first recovery. This test-budget
change does not repair that ownership mismatch, restart accounting or E6,
and does not complete M4.

Revert this test/CI change through the PR path to restore the previous grouping
and its observed timeout risk. There is no application state, schema or
infrastructure rollback.
