# M4 migration inspection field repair

## Scope and observed failure

This unit corrects two Docker inspect template selectors used by the guarded
generated migration process. It starts from PR #195 at
`f5a0006c52f0f682c215e688e95ffa90afea4c31`.

The [migration journey](https://github.com/tyhuang9/rig/actions/runs/39522876279/job/118970644889)
and the authority-revocation gate both stopped after container creation with
`migration_create_failed`. The recorded command counts were one create, zero
start, zero wait and zero remove. A failed ownership inspection also prevented
cleanup from establishing authority to remove the created container.

The original template mixed typed Go field names and JSON field names. Docker
CLI v28.0.4 first evaluates the template against its typed response, then tries
raw JSON after an evaluation error. The typed response contains `ID`,
`CPUPeriod` and `CPUQuota`; the corresponding raw JSON keys include `Id`,
`CpuPeriod` and `CpuQuota`. Therefore the old `.ID` plus `.HostConfig.CpuPeriod`
combination failed both representations.

Primary references:

- [Docker CLI typed inspection and fallback](https://github.com/docker/cli/blob/v28.0.4/cli/command/inspect/inspector.go#L92-L126)
- [Container identity field](https://github.com/moby/moby/blob/v28.0.4/api/types/container/container.go#L142-L175)
- [CPU resource fields](https://github.com/moby/moby/blob/v28.0.4/api/types/container/hostconfig.go#L341-L379)

## Change and invariants

The production edit changes only `.HostConfig.CpuPeriod` and
`.HostConfig.CpuQuota` to `.HostConfig.CPUPeriod` and
`.HostConfig.CPUQuota`. The emitted JSON keys remain unchanged.

The existing complete projection test now uses a Docker-shaped typed value with
the actual Go field names and JSON tags. It continues to execute the full
projection, reject environment disclosure, decode through the strict production
decoder and check absent network state.

Secret exclusion, complete/unique/typed-field validation, exact ownership
inspection, the final positive authorization check before start and conservative
cleanup remain unchanged. This repair introduces no schema, dependency,
configuration policy, runtime accounting or immutable history change. External
databases remain application-owned.

## Local acceptance evidence — 2026-10-21

All accepted Go commands used the absolute Go 1.26.7 toolchain,
`GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false` and
`CGO_ENABLED=0`.

The unchanged typed projection test failed on the old production template at
`.HostConfig.CpuPeriod`, then passed with the corrected selectors. The pinned
RED log SHA256 is
`0f56921daca599105db27db1a5c88851f0aeecac281b5d5fe3739594d0335b1d`;
the GREEN projection log is
`e96a8b799a9a9bac6d25207225795be3dfb81ba961e3f0109e60969d0ee3d921`.

| Platform | Selected package tests | Parents | Subcases | Result |
| --- | --- | ---: | ---: | --- |
| Windows amd64 | deployments | 11 | 81 | 92 passed |
| Windows amd64 | generatedexecutor authority | 3 | 2 | 5 passed |
| Native Linux amd64 through WSL | deployments | 11 | 81 | 92 passed |
| Native Linux amd64 through WSL | generatedexecutor authority | 3 | 2 | 5 passed |

Exact discovery, run and pass inventories match: 97 cases per platform, with no
failures or skips. Coverage includes malformed or incomplete inspection,
uncertain absence, canonical image/network refusal, reinspection before start
and remove, lost acknowledgments, post-observation authority drift and bounded
cleanup after cancellation. Linux binaries were cross-compiled and then executed
natively using literal WSL argument arrays. Binary hashes remained unchanged.

Additional checks passed:

- Read-only pinned `gofmt -d` on both changed Go files and `git diff --check`.
- `go vet ./internal/deployments ./internal/generatedmigration ./internal/generatedexecutor`
  for Windows and Linux.
- `go build` of `./cmd/hostd` and `go test -c -tags live_docker` of `./cmd/hostd`
  for both targets, with outputs outside the checkout.
- `pwsh -NoProfile -File scripts/check-generation.ps1` with pinned Go first in PATH.

All 975 tracked Go/module input hashes match before and after verification.
The manifest SHA256 is
`4dd71116e762a033be89c89f3ba852709b8b155202903e15ad4b0f6724cfadd6`;
the focused executable result is
`7a4a4963311dc5a28d27ee6b32854b9142a5e3a90be37f21d143b55b4a2b4f25`;
the consolidated receipt is
`aaf03760e8b3f5977ca37e8f81728a963e0fbf2b1043e0b7fa1f040e19d97776`.
The independent bounded source/security review found no required changes; its
report SHA256 is
`c4b5c0fe0029103b41f6f1f2fdded1cced022ee69fe8c828b980c4aa0f6cdd91`.

Raw command, inventory, exit and hash records are retained under the local
`m4-migration-inspect-fields-verification/final-20261021T035222Z` evidence folder.
An earlier unpinned Go 1.27 RED and unrelated accidental Go runs in a separate
checkout are preserved but excluded from acceptance. An initial Windows
PowerShell generation invocation could not resolve `Get-FileHash`; it changed no
source. The recorded PowerShell 7 retry passed. These deviations do not establish
performance evidence.

## Remaining acceptance and rollback

No real Docker command or live controller activation was performed locally.
Hosted CI must demonstrate the success/resume migration journey and the
authority-revoked-before-start journey, including their existing cleanup and
retention assertions. A typed fixture passing locally is insufficient to close
those Docker gates. Full M4 remains incomplete.

Reverting the two selectors and their typed regression restores the prior
incompatible inspection behavior. No data or schema rollback is involved.
