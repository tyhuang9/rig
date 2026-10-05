# M3 LAN rebind zero-claim successor preflight evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-passive-docker-live-gate` at `cdf00f5`

Branch: `feature/hosting-m3-rebind-preclaim-successor` (local and unpublished)

## Scope and invariant

The cutover contract requires successor interface and network proof before a
rebind claim is inserted. The existing successor preflight requires exactly one
prepared claim. This slice adds a separate read-only preclaim inspection that
uses the validated, unpersisted proposal and requires zero rebind claims. It
compares two complete SQLite, interface, host-route, Docker-network-ID and
Docker-prefix observations, then rereads SQLite after the second network
observation. It returns the proposed claim request digest and deterministic
successor network plan as advisory evidence.

The predecessor address may be absent or DHCP-drifted. A missing or ambiguous
successor, conflicting network, changed Docker identity, changed approval or
runtime head, cancellation, or lock-release failure rejects the result. No
claim, protected artifact, Docker effect, application HTTP request, URL, or
fence release is produced. The prepared-claim preflight is unchanged.

## Executable evidence

| Check | Result |
| --- | --- |
| `go test ./internal/generatedingress -run '^TestInspectGatewayRebind(PreclaimSuccessorPreflight\|SuccessorPreflight\|PreclaimPredecessor)' -count=1` | PASS after QA additions (24.156s). Covers both preclaim and existing postclaim paths. |
| `go test -count=1 -timeout=20m ./...` | PASS after final test edits, across all Go packages; generated-ingress completed in 115.095s. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web test` | PASS: 19 files, 502 tests. |
| `pnpm --dir web build` | PASS; Vite emitted a non-failing large-chunk warning. |
| `go build ./cmd/hostd` | PASS with a command-scoped Git `safe.directory` environment setting. The first elevated run failed VCS stamping because the sandbox-created worktree had different Windows ownership; `go build -buildvcs=false ./cmd/hostd` also passed during diagnosis. No global Git configuration was changed. |

The web install used `pnpm --dir web install --frozen-lockfile`. Its first
offline attempt lacked one tarball; a lockfile-respecting install then passed.
The default-sandbox web test run failed with `EPERM` while reading Vitest from
`node_modules`; the elevated rerun passed. Those were tool environment errors,
not failing application tests.

Tests confirm no persisted claim, protected-history change, or Docker mutation
on success. They require exact diagnostic codes for cross-observation drift and
within-inventory Docker identity drift, reject a prepared claim inserted after
the second inventory, and compare the preclaim result to the existing
prepared-claim result for the same proposal after claim insertion.

Independent security review found no new exploitable issue. It noted that the
unpersisted approval actor is supplied by the caller and checked for current
administrator role and digest, but the advisory result does not authenticate a
future effect. QA identified the diagnostic, late-claim, and phase-bridge test
gaps above; all were added and QA re-review found them closed.

## Remaining gates

This read may become stale after the final observation. A future effect writer
must authenticate approval, acquire the deployment-effects lease, repeat the
preclaim checks at its boundary and inside the immediate claim transaction,
then reattest the inserted prepared claim before any successor effect. The
hosted passive-Docker request-counter gate in parent `cdf00f5` has not run;
its local test skipped without Docker. Public preclaim/prepared-entrypoint
Docker acceptance, migration 034, transfer-aware readers, cutover and recovery,
Linux race, and physical second-device LAN acceptance also remain open.
Migration 033 remains dormant and unreleasable. No publication, merge, or
deployment was performed.
