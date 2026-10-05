# M3 prepared rebind successor intent evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-successor-identity` at `150623a`

Branch: `feature/hosting-m3-rebind-successor-intent` (local and unpublished)

## Scope and invariant

This slice defines a pure, digest-bound protected-intent value for the first
LAN gateway rebind. Its input is one prepared migration-033 claim, the
committed migration-026 fixed-name predecessor at its attested protected
history generation, ordered roster, stable successor-network preflight, and
the next-generation `v2-rebind-1` identity. A committed migration-026 upgrade
may be at generation greater than zero after earlier rollback or abort; the
constructor must bind that exact predecessor generation and reject generation
overflow.
Construction and validation must bind the claim and both approvals, exact
predecessor protected state and resource ownership, successor profile,
ordered roster entries, network plan, and successor identity. A substituted
field must fail even if a caller recomputes the outer digest.

This is an in-memory contract only. It does not write a protected artifact or
SQLite row, inspect or mutate Docker, create a claim, publish a URL, release a
fence, or change the existing v2 upgrade path. It does not support a later
rebind whose predecessor is a prior committed rebind; that requires a typed
source and terminal-receipt reader before effects can use this format.

## Executable evidence

The intent uses tagged profile and network projections for stable JSON field
names. Empty roster and entry-digest lists encode as JSON arrays. A fixed
SHA-256 vector (`f689ee4ccfa498d8ea245dd91e7c5febe801713e1183e474acb7d59723c8ff6f`)
pins the complete initial intent encoding independently of the constructor.

| Check | Result |
| --- | --- |
| `go test -count=1 -run '^TestGatewayRebindInitialSuccessorIntent' ./internal/generatedingress` | PASS after QA additions; independent QA rerun completed in 3.030s using workspace-local `GOCACHE`. |
| `go test -count=1 -timeout=20m ./...` | PASS after the production lineage correction, across all Go packages; generated-ingress completed in 130.048s. Live Docker tests skipped locally. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web install --frozen-lockfile` | PASS. |
| `pnpm --dir web test` | PASS: 19 files, 502 tests. |
| `pnpm --dir web build` | PASS; Vite reported an existing non-failing large-chunk warning. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | PASS with command-scoped Git `safe.directory` for this sandbox-owned worktree. No global Git configuration changed. |

QA verified empty, one-app, and two-app rosters, duplicate and ordering
rejection, claim/approval/predecessor/profile/network drift, forged digests,
legacy identity rejection, and isolation from caller slice mutation. Security
review found no exploitable issue and confirmed no production effect-path
caller. Its remaining provenance and freshness boundary is stated below.
The final full Go suite started before the last two QA test-only additions;
the focused suite passed again after those additions (3.861s in QA's rerun).
Manual code review found the original hardcoded generation, shared pointer in
mutation tests, and ignored fixture digest error. All were fixed and re-review
found no remaining issue. Security re-review accepted the generation binding
and flagged the future unused-generation check below.

## Remaining gates

An in-memory snapshot can go stale. A structurally valid history selection
does not prove that it is still the current head. A future effect writer must
hold the deployment-effects lease, repeat the strict history scan and
SQLite/protected/Docker and network proof at its effect boundary, write a
purpose-bound create-only intent, and read it back before resource creation.
It must also prove the derived next generation is unused; a prior rolled-back
rebind could have retained artifacts there while the predecessor remains at
its earlier generation.
The public preflight currently returns a selected network plan, without the
underlying inventory observation. A future persisted intent must capture and
bind that provenance as required by the cutover contract.
Hosted Linux Docker acceptance for both parent
passive attestors remains pending. Migration 033 remains dormant and
unreleasable. Migration 034, transfer-aware readers, cutover, recovery, and
physical second-device LAN acceptance remain open. This evidence does not
authorize publication, merge, or deployment.
