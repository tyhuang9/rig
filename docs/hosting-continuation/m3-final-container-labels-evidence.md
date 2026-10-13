# M3 final-container image metadata evidence

Date: 2026-10-12. Base: `cb83fa480c16eec33c8b38e9846ae421139bbce0`.

Purpose: make final-container ownership use the existing exact Rig-label
contract across typed handover and current attestation.

## Hosted observation

[PR #148](https://github.com/tyhuang9/rig/pull/148), head
`b94c469c07ad83ee03487e9a512f58b94e5b6526`, was tested at GitHub merge
`64f16ca8dc0076d7d2f9f51fbee12abbe7234dc0`.
[Runtime commit job](https://github.com/tyhuang9/rig/actions/runs/38262852349/job/114857513237)
ran the one expected parent and failed after 108.59 seconds. The first
attestation checkpoint was `runtime_final_container`; SQL was
`database_committed`, P17 was `handover_committed`, the commit receipt and
current artifact existed, and the successor was selected. Cleanup could not
acquire the retained gateway lock; its independent gate also failed.

Raw response SHA-256:
`5d300c6940c74caf2b2fe8c924ad75387184349fa78767aa698d5f3e50120775`.
The response and exact-checkout assessment are retained in the session evidence
folder `temp/pr145-cb83-pr148-b94-hosted-acceptance-20261012/`.

The logs identify the failed validator, not its exact field. They do not
establish that any particular image label appeared on that Docker container.

## Source defect and repair

The preceding typed handover validator uses `validGatewayV2ContainerLabels`.
The current/terminal final-ownership validator instead compared the entire
label map with `reflect.DeepEqual`. Thus a container with all exact Rig labels
plus foreign image metadata can pass handover and fail current attestation.

The repair changes only that predicate to the existing helper. Every expected
Rig label/value remains required, and every unexpected `io.rig.*` label is
rejected. Foreign labels confer no authority. Canonical ownership/configuration
digests, retained image/container identities, mounts, ports, hardening, liveness,
resource census, current-state proof and authorization stay enforced.

Complete label maps remain in repeated observation comparisons. A metadata
change between reads, including final-container name versus ID lookup, still
refuses. Terminal-only withdrawal retains its exact resource proof and guards.

## Executable local evidence

Go 1.26.7 cross-compiled the generated-ingress test binary with
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0`,
`GOFLAGS="-mod=readonly -buildvcs=false -p=1"`; WSL executed it with
`-test.v -test.count=1 -test.timeout=15m`. Discovery required every selected
parent. Commands, logs, exit codes, source hashes before/after and assessments
are retained under `temp/m3-final-container-labels-20261012/`.

| Check | Actual result |
| --- | --- |
| Existing Linux baseline | Two parents and four subcases passed; 11.313 seconds. |
| Regression before production repair | The metadata positive control, stable observation and two-app database-committed path refused. The 22 ownership negative controls passed. |
| Initial repaired selection | Eleven of twelve parents passed; the remaining parent exposed the new test's shallow mount-copy mistake. All 51 subcases passed. Overall command correctly failed. |
| Corrected affected parent | Cloned its mount slice before mutation; that parent and all 22 subcases passed, 1.984 seconds. The production file and other tests were unchanged. |

Four new parents cover exact Rig authority, stable current observation,
independent name/ID agreement and the actual two-app typed coordinator boundary.
The other selected parents cover immutable container/dynamic-network ownership,
mount permutations, invalid mounts, ordered-command drift, presence/liveness
mismatch and post-database-commit attestation. Across the repaired selection
and focused fixture correction, all twelve parents and 51 unique subcases have
passing evidence. This is not described as a single wholly passing invocation.

The first native Windows baseline attempt used installed Go 1.27.0 and failed
with `create generated builder root: Access is denied` before behavior
verification. That raw failure remains recorded; Linux execution supplies the
behavioral evidence. No test was skipped or silently omitted.

Read-only formatting checks (canonical LF bytes against Go 1.26.7 gofmt),
`git diff --check`, `go vet ./internal/generatedingress`, and
`go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe`
passed. Vet took 42.437 seconds and the four-command build 42.829 seconds;
all Go/module input hashes remained unchanged. No frontend source changed.

## Limits and rollback

This deterministic reproduction proves the source mismatch. Hosted Docker
forward commit/replay and complete cleanup must still verify the candidate.
Full repository/race and frontend suites were not rerun locally for this
focused predicate repair; their hosted gates remain required. M3 is incomplete:
supported full-controller crash recovery and physical second-device LAN
qualification remain separate.

Independent security review found no actionable vulnerability in the predicate
change, including its use by terminal-only withdrawal. Review is not Docker
acceptance. No factory, route, release flag, database service or deployment is
enabled. No durable history or authority is rewritten.

The dormant code change can be reverted. For an operation already past database
commit, preserve its receipts, SQL history, selected successor and fence;
recovery remains forward-only. A code revert is not permission to roll back
database authority or delete retained resources.
