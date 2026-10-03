# M3 protected progress attestation evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-progress-history` at `de25a22`

Branch: `feature/hosting-m3-rebind-progress-attestation` (local and unpublished)

## Scope and invariant

The prepared effect-boundary attestor holds the deployment-effects lease and
raw gateway lock while it compares two complete observations and a final
SQLite/protected anchor. This slice extends those comparisons to the exact
ordered pre-effect protected progress chain. An append or chain drift observed
between its snapshots and final anchor returns no evidence. An append after
the final anchor can make the result stale before return, because the private
progress installer does not acquire those locks. The returned result remains
private, scalar, read-only, unkeyed, and stale when the locks release. It is
not an authorization to change Docker, SQLite, or routing state.

Migration 034's prepared fence remains active. No guarded cutover writer,
terminal receipt, production caller, or resource effect is added here.

## Verification

| Check | Result |
| --- | --- |
| `go test -count=1 -run '^(TestGatewayRebindEffectBoundary|TestGatewayRebindProgress)' ./internal/generatedingress` | PASS, 24.215s before the QA-only drift addition. |
| `go test -count=1 -timeout=5m -run '^TestGatewayRebindEffectBoundary' ./internal/generatedingress` | PASS outside the restricted sandbox, 25.811s after QA added same-count drift and returned-evidence mutation cases. |
| `go test -count=1 -timeout=20m ./...` | PASS after the same-count drift case; generated-ingress completed in 159.054s. The final result-mutation test was added after this run and passed in the focused suite. |
| `go vet ./...` | PASS on the final Go source. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web install --frozen-lockfile` | PASS; lockfile unchanged. |
| `pnpm --dir web test` | PASS, 19 files and 502 tests. |
| `pnpm --dir web build` | PASS; existing non-failing Vite large-chunk warning. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | PASS with command-scoped Git `safe.directory`; global Git configuration unchanged. |
| `gofmt -l` and `git diff --check` | PASS, no formatting or whitespace defect. |

Tests use the real prepared SQLite/protected fixture and passive Docker and
network readers. They accept zero, one, and two valid progress records and
exact replay without changing private evidence. They reject an append between
full observations, an append after the second protected anchor but before the
final anchor, a same-count self-consistent record replacement, corrupt
progress, returned count/digest mutation, and release failures. Rejected
attempts return zero evidence and issue no Docker mutator commands. Source
search found no non-test caller for
the private attestor.

Security and QA reviews gave GO for this read-only scope. Manual code review
gave GO after narrowing the statement about drift to the snapshots actually
observed. CodeRabbit did not run because WSL authentication was unavailable.
The restricted sandbox produced the pre-existing `route_reconciliation_required`
lock failure before the attestor observation; the focused test passed outside
that sandbox. Local Go race testing was unavailable (`CGO_ENABLED=0`, no
`gcc`). Hosted Linux race, live Docker, and physical LAN acceptance remain
unverified.

## Remaining gates

The guarded writer must repeat all physical and cross-store observations while
holding its own lock chain. The stored image ID and topology digest are not
proof of live Docker or network provenance. Later protected phases and
receipts, transfer-aware readers, recovery, hosted Linux race and live Docker
gates, and physical second-device LAN acceptance remain open.
