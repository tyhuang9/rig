# M3 recovery follow-ups: combined verification

Date: 2026-10-16

## Purpose and source

Combine the reviewed recovery fixtures and diagnostic changes for one hosted
acceptance run. The base is PR175 at
`1736c38b34d17d172e459694b0618ed088c34b19`. Normal local merge commits preserve
all four inputs and their history:

| Input | Exact head | Scope |
| --- | --- | --- |
| PR176 | `a81cac5fdf7669c04fcdcbff196edb9fed92b70e` | Finite normal-replay refusal diagnostics |
| PR177 | `34351c5f6d505d600f943532cc023bad63e52f0f` | Exact persisted background-sentinel expiry |
| PR178 | `63a6b43ff7f9d79143b4bf50b53e289a6d0bab0c` | Finite deployment-failure classifications |
| PR179 | `3f841b0217ae65eaf525b5b7cb597df6e2a527c8` | Historically migrated two-claim LAN batch fixture |

The combined tested source is `a65d461ef9a60abfc26b452befb4b42db2ff9b69`,
tree `a7e5802eb339304169c364dc3beb61c1639362aa`. The four inputs change
disjoint files. No production source, migration SQL, dependency input,
authorization/proof behavior, cleanup ownership rule or runtime deadline
changes. The workflow adds the background-sentinel preflight control.

## Executed checks

Both platforms ran exactly 35 named parents and 309 nested tests, with one
run/pass per name and no skip, failure, missing parent or duplicate. The selector
is the 25-parent gateway preflight set including both normal-replay controls,
all six deployment-failure diagnostic parents, and these four LAN parents:

- `TestMigratedLegacyPairRecoveryComposition`
- `TestLegacyNativeBatchAuthorityExtractorRoundTrip`
- `TestLegacyNativePendingLANBatchMigratesExactGuardedClaims`
- `TestLiveHostdRebindNativeBatchIdentityControls`

Windows used Go1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`, the
`live_docker` build tag, the anchored exact-name selector, `-count=1`,
`-timeout=2m` and JSON events. Package time was 14.470s.

The same Go1.26.7 source was cross-compiled with `GOOS=linux GOARCH=amd64`
and actually executed under WSL with the identical selector, count and timeout.
All named run/pass outcomes match Windows. Compilation alone is not counted
as Linux execution. Neither local run provides race or Docker acceptance.

`pwsh -NoProfile -File scripts/check-generation.ps1` passed. Aggregate
whitespace, formatting of each changed Go Git blob, clean status and unchanged
`internal`, `db`, `go.mod` and `go.sum` checks passed.

The frozen external receipt `recovery-followups-with-lan-verification/verification.json`
records exact argument arrays, source Git blobs, raw-output
hashes and process results. Its SHA-256 is
`0de3134abf7ee6641b58ce562c4468323f148327496ca3f02ed51ce4bbca08e1`.

## Review and evidence limits

Each input has independent source and security/privacy or data-integrity
review. The LAN QA review's supplemental test execution used Go1.27.0 with
uncaptured environment. A separate root run at the exact standalone LAN head
used explicitly verified Go1.26.7 and recorded all four targeted parents and
seven nested cases passing. That run's raw JSON SHA-256 is
`63b5e424a72e241d8e3367694f951783c2f579cfb3cf242c333469ff0748e67d`.
The combined 35-parent run above also uses Go1.26.7. The Go1.27 result is
supplemental evidence only.

These changes repair two fixture assumptions and add diagnostics for two
unexplained hosted failures. They do not establish a production root cause for
normal retained replay or the early deployment failure. The actual historical
LAN public-state extraction, physical withdrawal of both ports, crash and
fresh-process protected identity still require the named Docker scenario.

The prior 38 individually accepted recovery cases belong to PR174's exact
source. Their acceptance does not transfer to this combined head. Full Docker,
race, recovery-matrix and physical second-device qualification remain separate;
M3 is incomplete. External databases remain application-owned and configured
through scoped runtime secrets; this integration adds no managed database or
Neon provisioning.

## Rollback

Revert the relevant test-only merge or abandon this draft branch. No production
data or infrastructure rollback is involved. Local integration and draft CI
publication do not authorize a GitHub target merge or deployment.
