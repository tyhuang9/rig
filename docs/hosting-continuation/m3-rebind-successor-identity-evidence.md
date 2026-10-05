# M3 LAN rebind successor identity evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-public-passive-docker-gate` at `b6f9609`

Branch: `feature/hosting-m3-rebind-successor-identity` (local and unpublished)

## Scope and invariant

The existing gateway v2 upgrade identity uses fixed Docker resource names.
The M3 cutover contract requires a distinct `v2-rebind-1` successor identity
for every protected generation and rebind operation. This slice defines that
identity as pure deterministic data with generation-scoped resource names,
short distinct container hostnames, a pinned image, exact config filenames,
successor profile binding, and a canonical digest.

The short hostname input tuple is encoded as a compact JSON array in this
order: `["v2-rebind-1", generation, rebind-operation-uuid, role]`, with the
generation as a JSON integer and role `f` or `s`. SHA-256's first 32 lowercase
hex digits form the hostname suffix. Golden tests pin the resulting bytes
independently of the production helper.

Validation must reject generation zero, noncanonical operation or profile
identities, wrong profile digest, legacy `v2` format, name or hostname
substitutions, and any identity drift even if a caller recomputes a digest.
The existing upgrade identity and its production readers and writers remain
unchanged. No Docker action, SQLite change, protected-history write, claim,
route change, URL publication, or fence release is introduced.

The identity binds the successor profile revision ID, revision number, and
spec digest. It does not authenticate configure operation ID, request digest,
or approver. Future protected intent and effect code must bind and verify those
approval fields against the claim separately.

## Executable evidence

| Check | Result |
| --- | --- |
| `go test -count=1 -run '^TestGatewayRebindSuccessor' ./internal/generatedingress` | PASS after the final test edits (0.685s). |
| `go test -count=1 -timeout=20m ./...` | PASS across all Go packages; generated-ingress completed in 126.155s. Live Docker tests skipped locally. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web install --frozen-lockfile` | PASS. |
| `pnpm --dir web test` | PASS: 19 files, 502 tests. |
| `pnpm --dir web build` | PASS; Vite reported an existing non-failing large-chunk warning. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | PASS with command-scoped Git `safe.directory` for this sandbox-owned worktree. No global Git configuration changed. |

QA independently recomputed both short-hostname vectors and the full golden
identity digest from the documented JSON encoding, then found the focused
tests sufficient. Security review found no exploitable issue in this pure
identity code and confirmed it has no production effect-path callers. It
highlighted the approval-binding requirement above for future wiring.
The full Go suite passed before a final comment and an additional focused
approval-boundary test; the focused suite passed again after those edits.
Manual code review requested that exact boundary comment and test, then
re-reviewed them and found no remaining issue. CodeRabbit was unavailable
locally because WSL returned `E_ACCESSDENIED` when checking its CLI.

## Remaining gates

This identity is not yet wired to protected rebind intent, a claim writer,
Docker observers or mutators, cutover, rollback, or recovery. Hosted Linux
Docker acceptance for the parent passive attestor gates remains pending.
Migration 033 remains dormant and unreleasable. This evidence does not
authorize publication, merge, or deployment.
