# M3 live-wait diagnostics harness evidence

## Scope

This harness-only change repairs the bounded observation and refusal diagnostics
used by the live hostd rebind scenarios. It leaves the runtime implementation,
database API, acceptance manifest, and hosted workflow unchanged.

- LAN scenarios open one observer before public disable requests and reuse its
  context-aware startup snapshot for each claim read.
- Claim observation and startup-refusal waits have a two-minute cap and preserve
  the existing cleanup reserve from test and fixture deadlines.
- Request outcomes retain only operation phase, HTTP status, and response or
  transport category. Cleanup cancels requests, reaps the exact child, then
  joins requests once under a shared 20-second budget.
- The configuration-drift refusal path opens its fence observer before any
  public mutation and reuses it for the bounded post-refusal read.
- Closed refusal diagnostics classify only a reaped child, use the existing
  sanitized stderr diagnostic, and reject normal, recovery-only, malformed, or
  oversized readiness markers.

The deterministic checks are named subtests of
`TestLiveHostdRebindChildStartupControls`, which is already in the exact parent
selector enforced by `hosting-gateway-v2-ci.yml`. No workflow selector change
was required.

## Local verification

All commands used the Windows Go 1.26.7 toolchain with
`GOTOOLCHAIN=local`, `GOFLAGS='-mod=readonly -p=1 -buildvcs=false'`, and
`CGO_ENABLED=0`. No Docker daemon or race compiler was used.

| Check | Result | Receipt |
| --- | --- | --- |
| `go test -tags live_docker -count=1 -timeout=5m ./cmd/hostd -run '^TestLiveHostdRebindChildStartupControls$'` | PASS, 9.556s | `windows-live-wait-child-controls-final.txt` |
| Exact 24-parent selector extracted from `.github/workflows/hosting-gateway-v2-ci.yml` | PASS, 12.571s | `windows-tagged-hostd-ci-controls-final.jsonl` |
| CGO-free Linux `cmd/hostd` test binary cross-compiled by the approved Windows Go toolchain, then executed in Ubuntu with `wsl.exe --cd /mnt/c/.../cmd/hostd --exec /mnt/c/.../hostd-linux.test -test.run <exact-selector> -test.count=1 -test.v -test.timeout=2m` | PASS | `wsl-tagged-hostd-ci-controls-final.txt` |
| Roster validation of the same selector | 24 parents each ran and passed exactly once on Windows and WSL | `hosted-selector-roster-validation-final.txt` |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | PASS | `windows-check-generation-final.txt` |

Receipts are retained outside the checkout at
`C:\Users\huang\Documents\Projects\Rig\temp\m3-lan-external-db-20261015\live-wait-verification`.

## Remaining hosted evidence

The local controls do not replace the M3 hosted Docker matrix. Before accepting
the repair, run the affected live LAN and configuration-drift matrix cases on
the exact integrated source, with the existing package deadlines and independent
cleanup checks unchanged. The configuration-drift hosted result also requires
the separately reviewed runtime guard that makes the refusal deterministic.
