# M3 rebind stopped stage-container Docker gate

Date: 2026-10-03

Base: `920f9f1` (`feature/hosting-m3-rebind-stopped-stage-container`)

Branch: `feature/hosting-m3-rebind-stopped-stage-container-docker-gate` (local and unpublished)

## Scope and acceptance invariant

This branch adds a hosted Linux Docker gate for the private successor Caddy
stage-container create. A disposable gateway fixture prepares the rebind claim
and uses the production host and Docker readers to bind the network, config
volume, data volume, and stopped stage container in protected sequences three
through six. No public rebind caller, application database provisioning,
configuration copy, container start, or route cutover is part of this test.

The test must prove that sequence six binds only the exact stopped container
identity, ownership digest, and create-configuration digest. Docker must show
the generation-scoped resource with the expected image, labels, security,
resource limits, volumes, configured network and host ports, but no effective
listener or network endpoint. Protected sequence-one-through-five bytes,
prepared SQLite claim, predecessor resources and route, selected host topology,
and routed application request count must stay unchanged. A fresh Manager
replay must preserve the complete history bytes and physical container identity
without issuing a second create.

Cleanup may remove only a container bound by an exact protected sequence-six
receipt after fresh physical ownership inspection. It must remove that stopped
container by ID without force and prove absence before removing the equally
verified data volume, config volume, and network. An unbound or uncertain effect
must remain for investigation and fail the CI residue gate. Hosted CI must
require the named live test to pass and reject a skip.

## Verification

| Check | Result |
| --- | --- |
| Base stage | Final-source full Go suite, vet, build, Docker-tagged compilation, and focused stage tests passed at `920f9f1`; see the stage evidence. |
| Live test discovery, tagged compilation, local skip | `go test -run '^TestLiveGatewayRebindSuccessorStoppedStageContainer$' -count=1 -v ./internal/generatedingress` discovered the named test and skipped before Docker access because `RIG_RUN_LIVE_GATEWAY_V2` is unset. `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` passed compilation. |
| Full Go suite, vet, build, formatting, workflow syntax, and diff check | Final-source `go test -count=1 -timeout=20m ./...` passed; `internal/generatedingress` took 312.091s. `go vet ./...`, `go build -buildvcs=false ./...`, `gofmt -l` for the new test, and `git diff --check` passed. The workflow parsed with the repository's `gopkg.in/yaml.v3`, with five jobs and the stopped-container job present. |
| Hosted Linux Docker gate and exact cleanup | Pending publication and CI. |
| Prerequisite network and volume hosted Docker gates | Pending publication and hosted CI. |

QA, security, and manual aggregate-diff reviews found no blocking issue. The
cleanup failure branches and uncertain-create outcome were not fault injected
in this live test; production ambiguity has unit coverage in the base stage.
CodeRabbit was unavailable locally, so the code-quality review used the
checked-out diff. None of these static reviews proves real Docker behavior.

## Remaining gates

This gate cannot prove Linux race behavior, stage configuration copy/start,
physical second-device LAN reachability, final cutover, terminal protected
receipt, transfer-aware SQLite transition, reviewed crash recovery, fence
release, or a public caller. The Docker-socket actor remains within the host
trust boundary and can race inspection or start the stopped container afterward.
The stage and this test cannot count as hosted acceptance until the draft stack
is authorized for publication and the named CI gate passes.

## Hosted checkpoint and inherited migration assertion correction

This branch was published as draft PR #128 at `a077ceb` under the user's
rows 26–38 authorization. On 2026-10-05 the inherited
[migration-uncertainty Docker job](https://github.com/tyhuang9/rig/actions/runs/37353748854/job/111910800922)
failed after 129.13s: its resume response was the exact requested job already
`assigned` at attempt two, while the test required `queued`.

`jobs.Service.Resume` commits, signals the live worker and then reads the job.
The response may therefore already be assigned or running. The test now
accepts those three in-flight states for the exact job ID. It still requires
worker interruption after the real migration, running attempt two, exactly two
migration executions and ledger/counter values, unchanged pinned provenance,
and restart recovery without replaying the migration. No production behavior
or migration authorization was changed.

Local verification with normal Windows filesystem access:

```text
go test -mod=readonly -p=1 -count=1 -timeout=10m ./internal/jobs -run '^(TestWaitingUserResumeStartsANewAttemptAndPausedCancelIsTerminal|TestResumeWaitsForConcurrentApprovalRevocationAndFailsClosed|TestComposeWorkerApprovalResumeRevocationRaceAndSingleDeployment)$'
go test -mod=readonly -tags live_docker -run '^$' ./cmd/hostd
```

The three resume/approval tests passed in 1.156s; Docker-tagged compilation
passed in 0.745s. Formatting and aggregate diff checks passed. Independent
read-only QA review confirmed the worker race and retained assertions. The
hosted migration test must pass at the corrected revision before acceptance;
tagged compilation does not prove the real Docker journey.
