# M3 protected rebind progress history evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-effect-boundary-attestor` at `58523f4`

Branch: `feature/hosting-m3-rebind-progress-history` (local and unpublished)

## Scope and invariant

This slice defines private create-only protected records for the first two
pre-effect rebind phases: `successor_intent` and `stage_intent`. Each record
binds purpose, version, generation, operation, sequence, timestamp, installed
protected intent digest, and its own canonical digest. The stage record also
binds the exact successor identity and resource names, the approved image
content digest, a separately observed normalized Docker image ID, the selected
network plan, and an observed topology digest. Sequence two binds the exact
sequence-one digest and requires a later timestamp.

The writer checks the currently installed protected intent and prior record
before it creates a file. It then rereads the entire strict rebind-aware
history. A write that may have installed but returns an error remains
uncertain; only a fresh exact replay can confirm it. The scanner rejects gaps,
operation collisions, malformed or case-folded reserved filenames, changed
files, purpose mismatches, and substituted intent or resource bindings. The
ordinary v1-to-v2 history scanner continues to reject all rebind artifacts.

These records have no production caller and do not authenticate Docker image
provenance or the host network. They do not authorize a resource effect,
SQLite transition, URL publication, or release of migration 034's prepared
fence. A future writer must repeat physical and cross-store observations while
holding the deployment-effects lease and gateway lock.

## Verification

| Check | Result |
| --- | --- |
| `go test -count=1 -run '^TestGatewayRebindProgress' -timeout=5m ./internal/generatedingress` | PASS after QA additions, 8.573s. |
| `go test -count=1 -timeout=20m ./...` | PASS on the final source after QA additions; generated-ingress completed in 153.801s. |
| `go vet ./...` | PASS on the final Go source. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web install --frozen-lockfile` | PASS; lockfile unchanged. |
| `pnpm --dir web test` | PASS, 19 files and 502 tests. |
| `pnpm --dir web build` | PASS; existing non-failing Vite large-chunk warning. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | PASS with command-scoped Git `safe.directory`; global Git configuration unchanged. |
| `git diff --check` and `gofmt -l` | PASS, no whitespace or Go formatting defects. |

Tests use real protected-history fixtures and a passive Docker runner. They
cover empty, one-record, and two-record scans; exact replay and ambiguous
post-install write; rejection before write for missing intent, stage-before-
successor, cancellation, wrong digest or resource binding, and invalid
timestamps or observation syntax; and rejection during scans for persisted
sequence gaps, unsupported later sequence, operation collision, malformed or
case-folded namespace, modified file bytes, and same-content inode replacement.
The success path asserts no SQLite change or Docker command. A source search
found the new constructors and store are called only by their tests.

Security and QA review gave GO for this storage-only slice. Manual code review
gave GO after narrowing documentation claims about supplied observations.
CodeRabbit did not run because its WSL CLI was unauthenticated. Local race
testing was unavailable (`CGO_ENABLED=0`, no `gcc`); hosted Linux race and
Docker gates remain unverified.

## Remaining gates

The later phases, terminal receipts, guarded SQLite writer, transfer-aware
readers, recovery, hosted Linux race and Docker gates, and physical
second-device LAN acceptance remain open. The observed Docker image ID and
topology digest are supplied values checked only for syntax. The successor
identity and selected network plan are checked against the installed intent.
This storage slice does not prove that either observed value came from a live
Docker or host-network observation.
