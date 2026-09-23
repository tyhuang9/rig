# Hosting continuation: M0 integration baseline

**Recorded:** 2026-09-23. **Base:** `main` at
`a8ce661c960662a5f1a4af343aeca81790844761`.
**Candidate:** `feature/hosting-m0-baseline`, branched from that base.
The primary checkout was clean before this work. This record describes the
checked-out code and observed checks; it does not authorize merge, deployment,
or publication.

## Integration identity and current state

| Capability | Implemented in source | Merged into `main` | Automated evidence | Live acceptance | Released |
| --- | --- | --- | --- | --- | --- |
| Official GitHub App defaults (#58) | Yes | Yes | PR CI and inherited tests | Real account/consent flow still required | No |
| Generated JavaScript runtime (#67, containing #60–#66) | Yes, opt-in | Yes | Linux Docker/race CI and local tests | One complete real source-to-container journey still required | No |
| Editable deployment setup (#69) | Yes | Yes | Local browser/API tests and inherited CI | Real repository journey still required | No |
| Persistent GitHub connector (#68) | Yes | Yes | Local browser/API tests and inherited CI | Real account/revocation journey still required | No |
| Scoped runtime/build configuration and external database hosting (M1+) | No | No | No | No | No |
| LAN listener, lifecycle controls, launcher, public domain/HTTPS (M3–M6) | No | No | No | No | No |

The verified merge order is #58 → #67 → #69 → #68 → #59. PRs #60–#66
were closed after their commits became ancestors of #67. The historical
connector branch at `0fc849e` is not the merged #68 source: its merged head
is `ca34831f079ff440619661bea816b68d977037aa`, which combines connector
and editable setup behavior. The merged #59 source is
`9269bfc8a915a0668fcf9ef2ef8705424f1e529f`. Its tree and `main`'s tree
are both `8d2cfdea06ec80a5366a730e86ef41bee62bee28`. No old feature
branch, migration, or protected revision was rewritten for M0. Live GitHub
showed zero open PRs when checked on 2026-09-23.

## Acceptance evidence

The following checks were run in the M0 worktree after pinning the generated
contract, Vite HTML input, and embedded text assets to LF on Windows. The
global Git setting `core.autocrlf=true` had made byte-for-byte generation
checks fail on the otherwise identical `main` checkout. The pinned files
contain no semantic source changes.

| Check | Observed result |
| --- | --- |
| `pnpm --dir web install --frozen-lockfile --offline` | Passed; 172 packages reused from the local store. |
| `go test -p 2 -count=1 -timeout=30m ./...` | Passed on base `main` and again on the M0 candidate. |
| `go vet ./...` | Passed on M0 candidate. |
| `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` | Passed on M0 candidate. |
| `pnpm --dir web test` | Passed on M0 candidate: 340 tests in 13 files. |
| `pnpm --dir web typecheck` | Passed on base; the candidate's embedded check also ran `tsc -b` successfully. |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed on M0 candidate, including migration mirror and OpenAPI route checks. |
| `pwsh -NoProfile -File scripts/check-embedded.ps1` | Passed on M0 candidate after rebuilding and comparing exact asset hashes. Existing large-chunk advisory remains. |
| `pnpm --dir web e2e` | Passed on M0 candidate: all 3 Chromium flows. |
| `pnpm --dir docs check:workflow`, `pnpm --dir docs build`, then `pnpm --dir docs check:accessibility` | Passed on M0 candidate. The accessibility check requires the completed build. |

The [#59 combined source CI](https://github.com/tyhuang9/rig/pull/59) passed
Documentation, Relay Compose lifecycle, Playwright browser, Windows controller,
Generated runtime lifecycle, and GitHub deployment workflows on exact source
`9269bfc`; the latter includes the hosted PostgreSQL/Linux race gate. Those
workflow results establish the merged application's source tree, since the
merged `main` tree is identical. They do not test M0's new line-ending rule on
hosted runners. A candidate PR must run its own checks before M0 is marked
ready.

| Acceptance case | Evidence and limit |
| --- | --- |
| INT-01 | Both merged flows are present. Source wizard, connector, manual setup, configuration, and plan-review tests plus 3 browser flows passed. Real GitHub consent remains a later live gate. |
| INT-02 | Existing Go tests cover exact configuration revision export, legacy release migration, protected bundle reads, and prior-release strategy pins. A copied production-like data root upgrade was not executed here. |
| INT-03 | Migration mirror/route generation check and full database tests passed. No historical migration contents changed. |
| INT-04 | Existing tests cover separate Compose/generated capability selection and exact plan dispatch. The prior combined-source CI includes hosted Compose and generated Docker runs; local Docker was unavailable. |
| INT-05 | Local candidate checks are recorded above and the exact merged source tree has prior hosted CI. Candidate-head hosted Docker/race checks remain pending PR execution. |

This Windows host has no Docker CLI, so it cannot prove a live container,
external database transaction, physical LAN client, or domain certificate.
No live Neon credential, database provisioning, or external mutation was used.
The post-merge `main` [Documentation run](https://github.com/tyhuang9/rig/actions/runs/35923212597)
failed at GitHub Pages configuration because the Pages site is not configured;
its build and accessibility checks passed. Pages settings and publication
remain untouched.

## Continuation decisions

These are constraints for M1–M4 implementation, not claims that the features
already exist.

1. **Configuration scope (M1).** Extend `internal/appconfig`'s immutable,
   purpose-bound protected bundles and SQLite metadata with explicit phase and
   component targets. Read version-1 bundles with their historical meaning;
   never rewrite their bytes, digests, or release pins. Generated server
   containers receive only their selected runtime keys, while public build
   values are explicitly non-secret and become part of the compiler's artifact
   identity. Compose keeps its current revision-export policy until a
   separately reviewed compatibility change. Migration execution keeps its
   existing explicit key allowlist and approval gate. Reject invalid scope and
   public-secret combinations before writing a new protected revision.
2. **LAN ingress (M3, with M6 layout considered).** Extend the existing
   generated Caddy gateway and persisted route state, which currently publish
   on `127.0.0.1` and match per-app `.rig.localhost` hosts. Allocate a stable,
   persisted per-app LAN listener only after explicit sharing consent; attest
   bind ownership and the exact application route before reporting a URL.
   Keep controller API/session traffic loopback-only and app networks private.
   Reserve a durable gateway storage layout for later certificates, but do not
   expose a public listener or claim HTTPS before M6 tests pass.
3. **Desired lifecycle (M4).** Store desired running/stopped state separately
   from observed Docker and route state. Stop withdraws traffic and prevents
   restart/recovery from reviving a stopped app before stopping owned slots.
   Start and restart must reattest owned containers and existing pins; an
   uncertain route or container result is visible and recoverable rather than
   silently reported as success. Preserve append-only jobs/events and release
   history, and never imply that stopping or reverting code rolls back an
   application-owned external database.

## Next milestone and rollback

M1 implements scoped server runtime secrets and public build values without a
managed database or Neon account API. It must first audit every current
configuration exporter, compiler input, and migration environment. Synthetic
secret-canary and external TLS database fixtures establish delivery and
non-disclosure before any live disposable-provider check is claimed.

The M0 checkout rule can be reverted without changing stored application
state. Future M1 bundle/schema changes require a backed-up data root and a
version-aware reader; binary-only rollback after writing a new bundle format
must not be assumed safe.
