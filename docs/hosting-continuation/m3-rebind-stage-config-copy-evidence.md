# M3 rebind stage-config copy evidence

Date: 2026-10-03

Base: `cbc1378` (`feature/hosting-m3-rebind-stage-config-intent`)

Branch: `feature/hosting-m3-rebind-stage-config-copy` (local and unpublished)

Code commit: `0143b51` (`feat(hosting): guard rebind stage config copy`)

## Scope and invariant

This slice guards the Docker write of the sequence-seven probe-and-404-only
Caddy configuration to the bound, stopped successor container. It may copy
only after exact protected, SQLite, predecessor, image, Docker, and host
reattestation under the deployment-effects lease and gateway lock, and strict
proof that `/config` is empty. It must read back byte-for-byte and append one
create-only sequence-eight protected receipt. If an earlier copy succeeded but
its receipt was uncertain, a fresh guarded invocation may adopt only the exact
content. Unexpected content or unreadable inventory remains fenced and is
never overwritten.

The operation does not start a container, publish a listener or route, write
SQLite, expose a public caller, or provision an application database. Protected
sequences one through seven remain byte-for-byte immutable.

## Verification

| Check | Result |
| --- | --- |
| Focused writer, scanner, archive, and failure tests | `go test ./internal/generatedingress -run '^TestGatewayRebindStageConfigCopy' -count=1 -v` passed before QA additions. A broader focused rebind and guarded-copy regression selection passed after QA fixes in 148.992s; archive/driver tests passed in 1.266s and sequence-eight constructor/forgery tests in 6.969s. |
| Full Go suite | Final-source solitary `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` completed in 421.833s. |
| Static and compile checks | `go vet ./...`, `go build -buildvcs=false ./...`, and `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed. Changed Go files passed `gofmt -l`; `git diff --check cbc1378` passed. |
| Web tests | Not rerun for this backend-only slice. The parent branch's `pnpm --dir web test` could not start because missing packages required registry downloads that returned `EACCES`; no web source changed here. |
| Live Linux Docker copy and readback | Not run locally; pending a separate hosted gate after publication authorization. The actual `docker cp` archive shape, stopped-volume write/readback, and physical cleanup remain unverified. |

QA rechecked the final source and found its wrong-byte driver issue fixed, with
the requested replay, retry, TAR, and stale-constructor cases present. Security
review found no material source vulnerability. The aggregate diff received
manual inspection; CodeRabbit was unavailable because the CLI was not
installed in WSL. Final integration review found no source blocker for local
branch review; that verdict does not establish hosted Docker acceptance.

## Remaining gates

Sequence eight ends with a stopped container. Hosted Docker must still prove
the physical copy and inventory format before a start/serving step. Stage
serving, cutover, terminal receipts, transfer-aware SQLite transitions,
recovery, fence release, public caller, Linux race proof, and physical
second-device LAN acceptance remain open. No hosted acceptance or deployment
is claimed by this local branch.
