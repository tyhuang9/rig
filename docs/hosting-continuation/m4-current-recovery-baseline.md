# Current recovery integration baseline

Status: local integration and focused combined verification passed; hosted
acceptance is pending. This baseline combines reviewed implementation and test repairs
for the same controller recovery journeys.

## Inputs and history

| Input | Published head | Role |
| --- | --- | --- |
| [PR #250](https://github.com/tyhuang9/rig/pull/250) | `e4450087fe251a6a7c3467e074a4d2df8e42787f` | Restore authenticated Current configured capacity accounting |
| [PR #246](https://github.com/tyhuang9/rig/pull/246) | `3a750657ab749e296d2a2191439e611f5bfaea9d` | Runtime fixture ownership, fresh replay and bounded legacy inspection |
| [PR #248](https://github.com/tyhuang9/rig/pull/248) | `35c2d038459cbf65268fe27365f247cef2b9298f` | Retained V2 process isolation and fixture-specific race allowance |
| [PR #249](https://github.com/tyhuang9/rig/pull/249) | `6786929578cd550e7b80a272ab5da7e17e283d78` | Closed cold-startup diagnostics in existing Docker journeys |

The local branch starts from PR #250 and preserves the other published inputs
through three ordinary merge commits: `6cd114f`, `fea2f16`, and
`89a30e33a059be68b282c2c529fb8c6e5010eefd`. All four input heads are ancestors
of that final integration commit. Its source tree is
`f6dee5adc5158e7486018bb112101fd76a430c15`.

Git combined the inputs without conflicts. The only shared file is
`generated_domain_registry_activity_test.go`: PR #248 changes the isolation
helper near the beginning, and PR #250 adds a capacity publication test later
in the file. The other input changes are disjoint.

There are 13 changed paths relative to PR #250 before this baseline document:
ten test files and three evidence documents. Every production source file,
workflow, selector and dependency manifest matches PR #250. This baseline adds
no new runtime behavior beyond that already reviewed input.

## Verification boundary

The individual input documents retain their exact tests, source/binary hashes,
failed development attempts and remaining hosted gates:

- [Restored capacity accounting](./m4-restored-configured-debt.md)
- [Runtime fixture ownership](./m4-runtime-fixture-ownership.md)
- [Retained V2 isolation and bounds](./m4-retained-v2-isolation.md)
- [Cold-startup diagnostics](./m4-cold-restart-startup-diagnostics.md)

Their independent results are retained separately from the fresh combined runs
below. No local Docker or Linux race execution is claimed.

## Combined local acceptance, 2026-10-26

Fresh Windows binaries were compiled with pinned Go 1.26.7,
`GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and
`CGO_ENABLED=0`. The ingress binary discovered 1,314 parent tests; the
`live_docker`-tagged hostd binary discovered 193. All 15 selected parents were
present exactly once. The tagged local controls do not execute Docker.

Each group used an anchored exact-name selector, `-test.count=1`, and
`-test.v`. The retained child processes kept their 150-second test deadline and
three-minute outer limit.

| Group | Selected parents | Test timeout | Actual elapsed | Result |
| --- | ---: | --- | ---: | --- |
| Capacity accounting and registry publication | 8 | 5 minutes | 2.787 seconds | PASS |
| Replay protocol and command audit | 3 | 10 minutes | 11.091 seconds | PASS |
| Authentic retained predecessor and refusal controls | 2 | 10 minutes | 220.090 seconds | PASS |
| Tagged hostd diagnostic and startup controls | 2 | 5 minutes | 5.557 seconds | PASS after runner correction |

The accepted logs contain 454 RUN/PASS pairs and 444 distinct test names,
including the 15 parents; there are no failures, skips or missing selected
parents. Repeated child names account for the difference between raw and
distinct counts. This is focused integration acceptance, not a full suite run.

The first hostd invocation failed after 5.101 seconds because the standalone
binary ran from the repository root. Its package-relative migration path
`../../internal/database/migrations` could not resolve. The same binary and
exact arguments passed from `cmd/hostd`, the working directory used by normal
package tests. Both attempts are retained; the failed attempt is excluded from
acceptance. No source change or other behavior rerun was needed.

Read-only Git-LF formatting, scoped ingress vet, tagged hostd vet, canonical
generation and diff checks passed. Source, input ancestry and exact input blobs
remained stable. Evaluating the 72 retained race/plain selector arguments
against fresh discovery showed 36 nonempty suites, identical race/plain
membership, and complete, disjoint coverage of all 1,314 ingress parents. This
selector evaluation did not execute the Linux shell contract or race tests.

Immutable local artifacts are retained under
`temp/m3-lan-external-db-20261015/m4-lifecycle-coordinator-verification/current-recovery-baseline-integration-20261026T142312Z`:

| Artifact | SHA-256 |
| --- | --- |
| `source-freeze.json` | `3f52cfbec56e9fb6212a28e712dbf01fce17e6e6e3cee95cb2f1c75e2fafa4d8` |
| `composite.json` | `1d474bf4c1d4f2c28328a6be16916a3e9382b46ee6a7f5e9f17c66b3dfd9535a` |
| `index.json` (43 artifacts) | `66bce308bc1440d6c11981f0c20a52d15bc2b3687cf96a8d792290df59c71185` |
| `behavior-census.json` | `dfb9a1e21b2b478453acd4aef4ee46b2e023d2326b53a276b3c4f1eaf51da821` |
| `static-receipt.json` | `577c0601d1aa291320a162147f160cc4c2ed5d51b64450f1693e9dc17a4fc7af` |
| Fresh ingress binary | `2de7bf78386291bfae23ef1e041cedbdf9c9fe1d4eefaa329b75346e3ee3bd36` |
| Fresh tagged hostd binary | `24741c3915e00c8b63359b4456d694434dcda0975a7ed9dc9ceccb0c98f908cf` |

The packet also preserves a rejected metadata composition with incorrect
property names, followed by the corrected composition above. This did not
change source or executable results. The new baseline document is outside the
source freeze and has its own documentation build and delivery receipt.

## Hosted acceptance still required

The earlier A0 Docker success on PR #247 is prerequisite evidence. The changed
cold construction path in PR #250 and this combined baseline need fresh hosted
evidence. Both retained V2 Linux race partitions must complete inside the
150-second child, 32-minute package, 34-minute step and 42-minute job limits.
Reaching the child deadline requires investigating the captured work before
another allowance change.

The remaining Docker checks include runtime fixture ownership and cross-store
replay, gateway upgrade/restart, cold migration/LAN/application-owned database
startup, and predecessor-retirement recovery. An older head's failure is not
attributed to this combined source without an exact checkout and log binding.
Closed startup diagnostics help identify a refusal; they do not authorize
recovery or establish its success.

Interrupted E6 states, later-worker recovery, full M4 acceptance and subsequent
milestones remain open. Databases remain application-owned and configured
through scoped runtime secrets; this baseline includes no managed database or
Neon provisioning.

## Rollback and operational limits

This is a local integration branch for review and hosted CI. The published input
branches and their immutable history are preserved. There is no schema migration
or new persisted capacity authority. Reverting an input through the normal PR
workflow restores its prior behavior and limitations while retaining durable
history. No target-branch merge, live controller refresh or deployment is part
of this integration.
