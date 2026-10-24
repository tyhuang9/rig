# M4 combined CI follow-ups

## Purpose and exact inputs

Combine the independently reviewed fixture, diagnostic and CI partition changes
on one tested source tree. This integration adds no runtime behavior beyond those
published inputs and preserves their commit history.

| Input | Published commit | Scope |
| --- | --- | --- |
| [PR #206](https://github.com/tyhuang9/rig/pull/206) | `5e6c130e9b42dfa736c448ea905f76ed1291b829` | Existing CI baseline and live stage fixture repair |
| [PR #207](https://github.com/tyhuang9/rig/pull/207) | `f3fe1c269105e996226bbfc86bc24fd875919980` | Ordinary ingress fixture schema reuse |
| [PR #208](https://github.com/tyhuang9/rig/pull/208) | `bf3821c4a319aeb87a450d8b665577109f44a65f` | Closed route pause diagnostics |
| [PR #211](https://github.com/tyhuang9/rig/pull/211) | `e2a00c9063dd6ba8b7f6d6997858b1a6fe2183b5` | Four disjoint runtime package race groups |
| [PR #212](https://github.com/tyhuang9/rig/pull/212) | `758166875eacb4941c3bfff81885913ecdf91d21` | Ordinary executor migration fixture schema reuse |

Local merge head `bd541c1d81e298662dea3f183f5ad56e38911446`, tree
`00eb09cc21dad71c0e6f43ecad1729223230d37f`, contains the exact union before this
evidence document. All four input heads remain ancestors. Their 22 changed paths
are disjoint; every changed mode/type/blob equals its owning input, and all
1,472 other paths equal PR #206. An independent audit checked all 1,494 entries.

The only shared package interaction is PR #208 diagnostics with PR #212's
ordinary fixture in `internal/generatedexecutor`. The existing whole-package
executor partition includes both. The ingress fixture and shared fixture helper
have no new interaction; their input evidence is retained rather than rerun.

## Preserved boundaries

- Receipt-owned removal and accounting safeguards are unchanged. No legacy
  removal bypass, route mutation fallback or public authority is introduced.
- The sealed candidate fixture is unchanged. Only the ordinary fixture openers
  reuse the existing isolated migrated schema; per-test authority state is fresh.
- All ten runtime packages remain selected exactly once across the four groups.
  All 26 ingress groups, race detection, count and time limits remain unchanged.
- External databases remain application-owned and use scoped runtime secrets.
  There is no managed database or Neon provisioning.

## Executed local verification — 2026-10-22

Pinned Go 1.26.7 used readonly modules and serial compilation. Linux test binaries
were cross-compiled and then executed natively through WSL. This is plain test
execution, not local race or Docker evidence.

| Check | Windows | Native Linux |
| --- | --- | --- |
| Whole executor behavioral parents | 55 passed | 55 passed |
| Nested executor cases | 77 passed | 77 passed |
| Separate exit-process helper parent | 1 passed | 1 passed |
| Exact discovery and matching RUN/PASS names | Passed | Passed |
| Controller compilation with `live_docker` | Passed | Passed |
| Executor/controller vet with `live_docker` | Passed | Passed |

Neither platform reported a failed or skipped case. The helper is counted
separately because its direct invocation does not test product behavior. The
compiler included all executor parents; no test-name subset was used. Execution
used `-test.run=^Test -test.count=1 -test.timeout=10m -test.v`.

The canonical `pwsh -NoProfile -File scripts/check-generation.ps1` and aggregate
`git diff --check` passed. All 1,050 Go/module hashes remained unchanged between
2026-10-22T20:50:27Z and 20:53:44Z.

The repository Bash contract also passed: 42 selector invocations, three exact
workflow matrices and 16 refusal/failure cases. It uses a temporary fake Go
runner, so those invocations are not Go test counts. Both Bash syntax checks
passed. The retained jq1.8.1 binary was used without installation.

The initial shell attempt stopped before execution after Windows CRLF-to-LF
normalization made Git report stale modified entries. The files already exactly
equaled their reviewed HEAD blobs, with an empty content diff. Refreshing only
those two index entries left the cached diff empty and status clean; the unchanged
contract harness then passed. The failed attempt and additive account remain
preserved alongside the successful receipt.

### Evidence identities

Local evidence is retained under
`temp/m3-lan-external-db-20261015/m4-ci-followups-combined-verification/`:

| Evidence | SHA-256 |
| --- | --- |
| Exact integration receipt | `86624f0771be100e0538a65e4e136709d634b9f327ab5bf6d460d109262ba4b0` |
| Independent whole-tree audit | `651b286e4b10903577e30b0f1c7defca8458e643278eb475d0b3404a344ae814` |
| Successful shell contract receipt | `d7f2974554803e37a6862b42327e74bd69b6ac51f702cd46ac705c3a9be15844` |
| Final executable/static receipt | `45e8afcf6761dc559265a6a92a5848bc145c7caaf8a976ba06c30c226e0157c3` |
| JSON source manifest | `38f2da901376e8938826fda84efca20c981c04c28b36351f3ef0a10aeb894e68` |
| Windows executor output | `cecc2ff124b72881840efefc79896146a69523de1b220741b4783996940774f4` |
| Linux executor output | `b15f08a7a8516ff62b191d70bb71f4d6866927a893183874bd6aab5e75f1727b` |

Documentation workflow, build and accessibility results are recorded separately
with the publication evidence. No source review or local result substitutes for
hosted Docker/race acceptance.

## Hosted findings and remaining work

PR #206's eight repaired Docker staging jobs passed all 11 selected parent tests.
Other route/restart journeys and race jobs remained failed or pending. This
integration does not relabel those results as passing.

PR #208's [LAN database job](https://github.com/tyhuang9/rig/actions/runs/39838080730/job/120031569338)
reported `previous_retirement` / `retirement_unavailable` during redeploy. Its
tested merge and published head have identical tree
`ebdd1527a46260a86d32f0f6fef12852af6dde43`. Four control parents passed; the live
journey failed. Raw log SHA-256:
`6abd1e9b174e068ccb07440d1d509d2f6e516c37adab8490b66b0d6bed630638`.

That diagnostic establishes the stage and shared error code. It does not identify
the rejected component, prove its receipt ownership, or distinguish the SQL
ownership guard from the Docker receipt-label guard. Source review confirms that
draining currently uses the legacy-only remover. Receipt-owned retirement needs
the planned domain-owned receipt/accounting handoff after its construction and
atomic consumer-cutover prerequisites. Both existing guards remain intact.

The startup classifier, private factory, route/retirement handoff and cold
accounting recovery remain separate implementation work. This draft does not
complete M4 or establish production readiness. No live controller was activated,
deployment performed or GitHub target branch merged by this integration.

## Rollback

Stop using this integration branch, or revert the relevant cohesive input change
through its normal review path. The integration adds no database schema,
immutable receipt format or production resource change to reverse. It neither
cleans up uncertain runtime resources nor releases retained accounting.
