# M3: verify the resumed pending-route deployment before serving

## Hosted acceptance cross-reference — 2026-10-18

The [M3 hosted acceptance addendum](m3-hosted-acceptance-20261018.md) preserves the earlier PR #185 failure and records the repaired PR #186 pending-route result: 606.50 seconds, exact deployment resume after process restart, historical COMMIT/replay/durable proof, A=v2/B=v1 LAN matrix, child reaping and cleanup. The old failed HTTP body was not retained and is not inferred. Physical second-device qualification and manual screen-reader testing remain open.

## Failure and scope

PR #185 at `395eb5bb408a4f81384ca03092a81159705f8da3` reached a later
failure in the real Docker pending-route restart case:

- [Run 39116235131, job 117652821298](https://github.com/tyhuang9/rig/actions/runs/39116235131/job/117652821298), attempt 1.
- All 356 preflight controls passed. The case recorded the historical COMMIT
  checkpoint, passed the superseded replay and historical durable checks, then
  failed its LAN `/api/version` assertion after 610.82 seconds.
- HTTP status was 200 as expected, but `supported-rebind-a-v1` was absent.
  The response body was not retained; the actual app/version is unproved.
- The runner reported no timeout or residual process group. Independent fixture
  cleanup passed. The seven later cases in partition 06 did not execute.
- Raw log SHA-256:
  `6965f6f8329740c5da3c07ee196774ffb6e6b96c070ef2dbfa8d16f647746282`.
  Failure artifact SHA-256:
  `f918ff64f5d2db2ae8cabe05ffabf4fd98d83e4a529db174209c16a1e7b116ac`.

The scenario deliberately accepts a deployment pinned to app A's v2 scoped
configuration. Startup restores the previously committed route, then starts the
worker that can resume that deployment before normal readiness. The final
generic serving helper instead expects A=v1/B=v1, without waiting for the
accepted deployment's final state. This is a stale test expectation and a timing
gap. Actual v2 serving in the failed run remains an inference from source.

## Required contract

The repair is confined to the live test harness:

1. At the held lost-acknowledgment boundary, capture a fresh durable snapshot
   bound to the exact accepted job, deployment, plan/configuration pins,
   candidate resources, and previous active head. The boundary marker can
   arrive before the observer's first poll, so a prior polling result alone is
   insufficient.
2. After restart, wait within the existing budget for that same deployment to
   succeed and become the exact current head. Reject identity drift, terminal
   failure, incomplete success, child exit, observer error, or timeout. Keep
   identifiers, job input, and resource details out of diagnostics.
3. Preserve the historical gateway replay and durable checks. Require the full
   existing LAN isolation matrix with A=v2 and B=v1 on the successor.

The generic v1/v1 helper remains applicable to other historical rebind cases.
Production recovery, runtime safeguards, workflows, deadlines, immutable history,
and deployment admission are unchanged. The scenario must not create another
deployment, accept either marker, or extend its deadline to obtain a pass.

## Verification

The failed hosted case above is the regression evidence. Final local checks on
2026-10-18 used Go 1.26.7 with `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`:

| Check | Executed result |
| --- | --- |
| Existing 25-parent workflow preflight: `go test -tags live_docker -json -count=1 -timeout=2m ./cmd/hostd -run "$test_pattern"`, using the unchanged selector in `hosting-gateway-v2-ci.yml` | 387 named tests ran/passed once; zero failures/skips; 26.62 seconds wall time |
| Linux amd64 test binary built with `go test -tags live_docker -c ./cmd/hostd`, then executed under WSL from `cmd/hostd` with `-test.count=1 -test.timeout=2m -test.run='^(TestLiveHostdRebindChildStartupControls)$'` | 116 named tests ran/passed once; exact Windows subset; zero failures/skips; 18.89 seconds wall time |
| `gofmt -l` on both changed Go files | Empty output |
| `git diff --check` | Passed; only Git line-ending notices on stderr |
| `go vet -tags live_docker ./cmd/hostd` | Passed |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed |

The new controls cover a marker observed before the first snapshot, pending then
exact convergence, changed identities/pins/current head, terminal failures,
observer errors, timeout, and exact child exit. They also force cancellation or
child exit during a snapshot that returns an otherwise valid success. A real
SQLite fixture distinguishes the exact configuration and current head from
foreign records and verifies private-field redaction.

Final Windows raw log SHA-256:
`56d2133a0d06b46cdee8368fbaa3798423af6727809c677966de0c2d45d42d1c`;
scope `6bae4822786fea630ea3f476afb70690dad17e8ae7595df122eec01c67a4025e`.
WSL raw log SHA-256:
`d5c9033cc8d77be79fc26647b75d8b4847855b050bfb083d33610c57722a82d6`;
scope `71cb950c7ff339d199394b49eb094c54e3a7ac89b67a7a1b71b0aa9d8e3b4d7d`.
All three verification source bindings match
`113ef768aa53db11c06770c14def55a64ed5ca41f490355c4d0d31cb9e698554`.

Earlier local results remain recorded separately: the first Go run passed 385
names but its wrapper rejected an incorrectly expected count of 386. Review then
found a post-snapshot timeout/child-exit gap and missing real adapter coverage.
The first correction failed two fixture checks (the cancellation label and an
unfinished prior synthetic job). Those fixtures were corrected before the final
387-test run above. No failed result is represented as acceptance.

Hosted Docker execution on the repaired revision is still required. Earlier
revision passes, local controls, and a successful draft push do not establish
the pending-route case, all 63 matrix cases, physical second-device LAN
acceptance, or completion of M3.

## Rollback

A reviewed revert restores the prior harness. This change has no runtime rollout,
data migration, controller activation, or target-branch merge.
