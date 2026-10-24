# M4 current-serving fixture admission

## Status

Local Go verification passed on Windows and native Linux. Hosted race
verification remains pending.

Base: PR #209, `491219a6abb14e540a725759654ff96ca0044bf1`.

## Hosted failure

Windows current-serving job `120033725980` ran the synthetic merge
`ff354e9bd5b0f6be1a88bc6180943acbddc484a1`. Its tree
`4d7856ae47043e5c55601b22527e69f57783efb6` equals the PR head's tree.
The package recorded 43 parent tests (36 passed, seven failed) and 133 child
tests (130 passed, three failed), with no skips.

The seven failures occur in public LAN state-machine tests. Their shared raw
Manager fixture has no domain view. Typed mutation therefore refuses admission
with `route_reconciliation_required` before the fake lease, physical driver or
callback can exercise the intended behavior.

PR #215 fixes the same class of omission in the separate commit-recovery fixture.
It does not change these current-serving call sites.

## Repair scope

A local test helper prepares real controller directories and constructs the
Manager with `New`, retaining the fixture's data root, current-state repository
and fence configuration. Construction must issue zero daemon commands. The
helper preserves the test latches and recording physical driver needed by the
existing assertions.

Only nine call sites in `gateway_current_lan_state_machine_test.go` use the
admitted helper: five direct test fixtures, three cancellation/lost-write child
cases and the lost-postcommit-proof case. The shared operation fixture stays
unchanged, so this repair does not alter PR #215's commit-recovery chain or the
pure transition tests.

The existing assertions continue to require successful publication, activation,
rollback, quarantine, exact retained state and intended authority-loss refusal
at their respective boundaries. Production admission and authorization behavior
are unchanged.

## Verification

The exact hosted failure supplies the pre-change executable baseline. Fresh
Windows and Linux package binaries each discovered and passed all seven affected
parent tests and three existing child cases: zero failures and zero skips.
The existing callbacks, lease/driver counts and retained-state assertions all ran.

| Check | Result |
| --- | --- |
| Pinned `gofmt -l` on the changed test file | Pass |
| `go test -c -o <recorded binary> ./internal/generatedingress`, each target | Pass |
| Exact seven-parent discovery and execution, each native target | 7 parents / 3 children passed |
| `go vet ./internal/generatedingress`, Windows and Linux targets | Pass |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Pass |
| `git diff --check 491219a6abb14e540a725759654ff96ca0044bf1` | Pass |

The run used Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`, and explicit
`GOOS`/`GOARCH=amd64`. Linux binaries executed in WSL Ubuntu. No Docker or race
execution was available locally; these results do not replace hosted acceptance.

Evidence SHA-256:

- Final executable receipt: `66d7ed3171523278e4d8230d0305317b72ece62b88c371220a95ef1b23b67232`.
- Windows verbose output: `10e8617817916135db172bc3c0df0242fbfbdf7459e07036aaa7dc103067f2f0`.
- Linux verbose output: `3468f2cc2d9fac4001b82f4fa58ec5c82c3b2c4d14f1c681396696f42b873459`.
- Final test source: `d12232b1b963252c1ffe8a8b46625585d3b6594ac877ecf81fd2076c50f86e32`.

All 1,053 Go/module source hashes were unchanged during final verification.
The initial format-only attempt stopped before compilation or tests because of
CRLF line endings; the retained format transition proves that normalization
changed no other bytes. The final run above passed independently.

## Risks and rollback

This is a test-fixture change. Its main risk is changing fixture setup enough to
hide the intended uncertainty branch; positive lease/driver/callback counts and
unchanged retained-state assertions are required to prevent that.

Reverting the test helper and its call sites restores the prior fixture. No
production data, runtime resource, controller configuration or database changes
need rollback.
