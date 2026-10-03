# M3 protected rebind successor intent evidence

Date: 2026-10-03

Base: local `feature/hosting-m3-rebind-successor-intent` at `e9e5ace`

Branch: `feature/hosting-m3-rebind-protected-intent` (local and unpublished)

## Scope and invariant

This slice adds a private, create-only, purpose-bound successor intent bundle
for the initial rebind from a committed migration-026 fixed-name gateway. The
bundle binds the prepared migration-033 claim snapshot, exact protected
predecessor, selected successor generation and identity, ordered roster, and
the private host/Docker network observation used to select the network plan.
The shared roster JSON omits `EntryDigest`; strict readback restores each
digest from the separately persisted ordered vector and recomputes it before
accepting the bundle. This internal readback checks structure and digests; it
does not authenticate the source.

The rebind-aware history scan separately compares the roster's exact LAN app
set and every overlapping allocation, grant, access, profile, port, and slot
field with the committed protected predecessor.

An exact replay reads the protected bundle back. A different operation cannot
reuse an occupied generation. A legacy upgrade/ownership scan fails closed
when any rebind intent artifact is present, including malformed or case-folded
filenames on Windows.

The rebind-aware reader accepts at most one exact intent. It does not infer an
abort or rollback from an occupied generation. An installed intent remains
unresolved until typed terminal receipts and guarded recovery are implemented.
The bundle is preparation evidence only: no production call path installs it,
and it does not authorize Docker, SQLite, network, routing, or URL effects.

## Executable evidence

| Check | Result |
| --- | --- |
| `go test -count=1 -run 'TestGatewayRebindInitialSuccessorIntent|TestGatewayUpgradeHistory' ./internal/generatedingress` on the parent before edits | PASS, 3.605s (implementation baseline). |
| `go test -count=1 -run 'TestGatewayRebindProtectedIntent|TestGatewayHistoryRejectsCaseFolded|TestGatewayRebindInitialSuccessorIntent|TestGatewayUpgradeHistory' ./internal/generatedingress` | PASS after the protected-roster comparison and forged-source test, 17.274s. |
| `go test -count=1 -timeout=20m ./...` | PASS on the final protected-roster fix outside the restricted filesystem sandbox; `internal/generatedingress` completed in 125.306s. |
| `go vet ./...` | PASS. |
| `go test -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedingress` | PASS compilation only; no live Docker test ran. |
| `pnpm --dir web install --frozen-lockfile` | PASS; lockfile unchanged. |
| `pnpm --dir web test` | PASS, 19 files and 502 tests. |
| `pnpm --dir web build` | PASS; Vite reported its existing non-failing large-chunk warning. |
| `go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | PASS with command-scoped Git `safe.directory` for this sandbox-owned worktree; no global Git configuration changed. |
| `gofmt -l` on the four changed Go files and `git diff --check` | PASS; no formatting or whitespace findings. |

The restricted filesystem sandbox caused an unchanged parent test,
`TestSwitchPersistsAcceptedRouteAfterValidatedReload`, to fail with
`ingress_drift_detected`; the candidate failed the same test there. The same
parent test passed outside the sandbox, and the candidate's full Go suite
passed outside it. The sandbox also denied web package reads/fetches; the
lockfile install, web tests, and build passed outside it. These environment
failures are not counted as code acceptance results.

Focused tests cover one-app and two-app roster install/load/history, wrong
in-memory and persisted entry-digest rejection, a self-consistent forged
allocation that loads but fails the predecessor history scan, exact replay and
readback, a possibly installed write followed by fresh replay, generation
selection and occupancy, stale claim and plan-only observation rejection,
recomputed forged digests,
wrong protected purpose, malformed/unknown/case-folded filenames, generation
gaps and collisions, operation substitution, orphan intent, nonregular and
changed artifacts, and legacy scanner rejection. The two-app fixture
synthesizes a second claim/grant/LAN route and updates its test protected
state; it does not prove a second SQLite application setup journey. The
symlink artifact subtest was skipped locally because Windows denied symlink
creation. Hosted Linux execution is still needed for that case and the parent
Docker gate.

Security review gave GO for this storage-only scope after the Windows
case-folded namespace and protected-roster fixes. The protected store is
private; production call-site inspection found no effect entrypoint.

## Remaining gates

The stored database digest is not a fresh database attestation. The Docker
network observation retains stable IDs and aggregate prefixes, not a per-ID
prefix map. A future coordinator must hold the deployment-effects lease and
gateway locks, reconstruct the exact current SQLite, protected-history,
Docker-ownership, and network evidence, and compare the canonical intent
before any external effect. It must treat an ambiguous protected write as
unresolved until exact readback.

Typed abort/rollback and later committed-rebind source readers, progress and
terminal receipts, migration 034, transfer-aware authorization, cutover,
recovery, hosted Linux Docker/race gates, and physical second-device LAN
acceptance remain open. Migration 033 remains dormant and unreleasable. This
evidence does not authorize publication, merge, or deployment.
