# M3 rebind stage-config intent evidence

Date: 2026-10-03

Base: `d54caa8` (`feature/hosting-m3-rebind-stopped-stage-container-docker-gate`)

Branch: `feature/hosting-m3-rebind-stage-config-intent` (local and unpublished)

Code commit: `57a80f8` (`feat(hosting): pin rebind stage config intent`)

## Scope and invariant

This slice prepares a deterministic successor Caddy stage configuration and
records its exact content and destination in protected sequence seven. The
configuration exposes only the approved successor-address probe and 404
responses for the selected ports; it does not proxy application traffic.
Sequence seven binds the stopped stage container, config volume, protected
successor intent, and predecessor digest already proved in sequence six.

Before appending the intent, the private writer must hold the deployment
effects lease and gateway locks, reattest the prepared claim and all existing
protected and physical predecessor/successor state, and obtain a successful
bounded inventory proving the stopped container's `/config` directory empty.
An error or uncertain archive is never treated as proof of emptiness.
The writer has no Docker write, container start, route change, SQLite
transition, public caller, or application database provisioning effect.
Earlier protected records and their bytes remain immutable; replay may only
accept the exact recorded intent.
Sequence seven is a v1 config-byte format: scanning regenerates its content
digest from the current code. Its empty-route Caddy JSON is pinned by a fixed
input golden length and SHA-256 test. A future config format must keep v1
generation for existing records and introduce an explicit version before
writing new records. A second fixed-input golden pins the v1 probe-token
context and field projection used to build those bytes.

## Verification

| Check | Result |
| --- | --- |
| Focused constructor, scanner, writer, and archive tests | `go test -count=1 -timeout=20m ./internal/generatedingress -run '^(TestGatewayRebindStageConfigV1Bytes\|TestGatewayRebindProgressSequenceSeven\|TestGatewayRebindProgressHistoryRejectsForgedSequenceSeven\|TestGatewayRebindProgressSequenceSevenRejects\|TestGatewayRebindStageConfig\|TestGatewayRebindExactEmpty\|TestGatewayRebindStageContainerCreatesBindsAndReplays)'` passed in 79.813s, before the pure v1 probe-token helper was added. The final-source byte and probe-token tests passed with `go test ./internal/generatedingress -run '^(TestGatewayRebindStageConfigV1Bytes\|TestGatewayRebindStageConfigProbeTokenV1\|TestGatewayRebindStageConfigIsDeterministicProbeAnd404Only)$' -count=1`. |
| Reported concurrent test failure | A reviewer running a broad subset concurrently saw `route_reconciliation_required`, including in unchanged sequence-four cases. The sequence-seven append/replay and sequence-four stale-authorization tests each passed when rerun alone. The final full suite also passed alone, so no reproducible regression remains from this signal. |
| Full Go suite | Final-source solitary `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` completed in 365.610s. The earlier pre-golden run also passed. |
| Static checks | `go vet ./...`, `go build -buildvcs=false ./...`, `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress`, `gofmt -l` on changed Go files, and `git diff --check d54caa8` passed. |
| Web tests | `pnpm --dir web test` could not start because this worktree lacked installed packages and registry fetches returned `EACCES`; the retrying install was stopped. No web source changed in this slice. |
| Live Linux Docker inventory behavior | Not run locally; pending a separate hosted gate after publication authorization. The exact `docker container cp` archive shape is unverified on Linux. |

Manual code, QA, security, and final integration reviews found no remaining
source blocker after the TAR truncation fix, added drift cases, and fixed v1
byte/probe-token goldens. These reviews are separate from executable Docker
acceptance.

## Remaining gates

Sequence seven does not copy configuration. A separate guarded copy and exact
readback receipt, then a hosted Docker test, are required before a stage start.
Stage serving, cutover, terminal receipts, transfer-aware SQLite transitions,
recovery, fence release, public caller, Linux race proof, and physical
second-device LAN acceptance remain open. No hosted acceptance or deployment
is claimed by this local branch.
