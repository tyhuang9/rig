# M3 fresh recovery diagnostics

## Observed hosted failure

Draft [PR #149](https://github.com/tyhuang9/rig/pull/149) at source
`b591f042c9569742ee66f45863cb3ac938fb5b75` ran against base
`cb83fa480c16eec33c8b38e9846ae421139bbce0`. GitHub's test merge
`489da5ba9f6349e20b32badd023d2bd7816e2cb5` has those parents and tree
`6c1b87ef8b447c5d76aee854266d02e004cad304`, matching the local source tree.

The [forward runtime job](https://github.com/tyhuang9/rig/actions/runs/38458172640/job/115506168224)
failed after 155.62 seconds. Fresh-manager recovery returned a zero result and
three joined `route_reconciliation_required` errors. Cleanup refused its gateway
lock approximately 1.6 milliseconds later and retained the resources. This run
did not reach the initial cleanup-attestation diagnostic. The previous source
`0cfb94f` had passed fresh replay and failed later in cleanup; those results must
not be attributed to this run.

The fixture has a 30-minute context. Source inspection ties the immediate cleanup
refusal to the shared fail-stop latch following guarded recovery withdrawal;
the log does not establish a leaked lock. The joined errors narrow the original
failure to protected-history reads, predecessor retirement, committed-current
attestation, or final authority/fence confirmation after current selection.
The production root cause remains unproven.

On the same source and test merge:

| Gate | Result |
| --- | --- |
| [Rollback and replay](https://github.com/tyhuang9/rig/actions/runs/38458172640/job/115506168133) | One parent passed without skips; independent cleanup passed. |
| [Stopped successor stage](https://github.com/tyhuang9/rig/actions/runs/38458172640/job/115506168423) | One parent passed without skips; independent cleanup passed. |
| [Windows ingress remainder](https://github.com/tyhuang9/rig/actions/runs/38458172782/job/115506168386) | 288 parents: 263 passed and 25 expected opt-in/platform skips. All six TLS parents passed. |
| [Controller lifecycle race](https://github.com/tyhuang9/rig/actions/runs/38458172619/job/115507233132) | All 26 parents ran and passed without skips. |

Raw job metadata, logs, test inventories and source-merge proofs are retained in
`temp/m3-pr149-cleanup-ci-20261013/`. Log SHA-256 values:

| Job | SHA-256 |
| --- | --- |
| 115506168224 | `d272dc77997907b092df9b4284c1495e2731721535069ba286c9abc6944721a8` |
| 115506168133 | `eb34720177f3a1ae5a5ab3859e3d549c3f2fcca181dd19b9735d85644316f2d7` |
| 115506168423 | `5eaa9ed844359ae8ee8b198bd8c9673e5c222c76dc6c032c5b12906d92830689` |
| 115506168386 | `bad2005a432e9eea1e3c9cef23fd5fb1e736f8725c56983bd32321ebbc691c8f` |
| 115507233132 | `792abe1f9b30f60e49768a3b3b2a86a48c8f4c43a40d2aa8375a29d26ac3bcf6` |

## Local baseline

Before diagnostic edits, `TestGatewayRebindConcreteCompositionCommitsAndReplaysSameDockerState`
and its two descendants passed with no skips. The descendants exercise stopped
terminal restoration and exact-owner withdrawal after terminal-config corruption.
The suite checks SQL, protected history, admission latches and physical effects.
All 889 Go/SQL/module inputs remained unchanged through compilation, exact test
discovery and actual WSL execution. Compilation took 3.578 seconds, discovery
3.906 seconds, and execution 19.312 seconds.

Commands used Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, Linux amd64 and `CGO_ENABLED=0`:
`go test -c -o <binary> ./internal/generatedingress`, anchored `-test.list`, then
`go tool test2json -t -p github.com/hostd/hostd/internal/generatedingress` wrapping
actual WSL execution with `-test.v=test2json -test.count=1 -test.timeout=10m`
and the anchored parent name.

Binary SHA-256: `1f2fa3929525ae5ac457a4844b7ab7f9bdb54357be9a4f883bc752f614693653`.
Test log SHA-256: `dc1527f23ee04d78031a83e810eba56bb784223b8b72d21d6b93de70cffa7c6c`.
Exact commands and per-input hashes are in
`temp/m3-fresh-recovery-proof-20261013/fresh-recovery-baseline-wsl.*`.

## Diagnostic change

A private test helper wraps the installed current driver and cross-store driver
around exactly one existing startup-recovery call. It records retirement and
attestation entries, successes and failures, digest validity, and a validated
member of the existing attestation-checkpoint enumeration. Its output contains
only fixed fields, booleans, counters and finite checkpoint names. It does not
retain raw errors, resource identifiers, command arguments or authority values.

The cross-store wrapper delegates through the exact installed interface, preserving
the live and composition fixture overrides. The current wrapper embeds the
existing concrete managed driver, preserving its optional interfaces. Both
installed values are restored with `defer`. The recovery result and error pass
through unchanged. No retry, production path, guard, authority rule, migration,
runtime secret or physical effect is added.

The live commit and rollback journeys use this helper. The existing composed
replay test also exercises it for successful recovery, stopped-terminal recovery,
and corrupt-config withdrawal, retaining the effects, SQL and history checks.
Six new contract subcases cover retirement and attestation success/failure,
single recovery delegation, result/error preservation and driver restoration.

## Final local verification

The final formatted source passed both parents and all eight descendants, with
exact discovery, matching RUN/PASS inventories, and no skips or missing tests:

- `TestGatewayRebindConcreteCompositionCommitsAndReplaysSameDockerState`:
  `stopped_terminal_restores_exact_successor` and
  `corrupt_terminal_config_withdraws_exact_owner`.
- `TestGatewayRebindRuntimeRecoveryDiagnosticDelegatesWithoutMutation`:
  `retirement_pass`, `retirement_failure`, `attestation_pass`,
  `attestation_failure`, `recovery_pass_restores_exact_drivers`, and
  `recovery_failure_restores_exact_drivers`.

All 890 Go/SQL/module inputs remained unchanged. Compilation took 8.750 seconds,
discovery 2.079 seconds, and actual WSL execution 20.015 seconds, using the same
commands and environment as the baseline with an anchored two-parent list.
Binary SHA-256: `a44c586888cc1be33366bfc379b6e87f53aa7499935b2f1f3dc3b7e0976e0475`.
Test log SHA-256: `c19c367ceb31c686216fcbd1fc68eab1d96a192df64ca197396e2ffa8ed3b03a`.
Receipts: `temp/m3-fresh-recovery-proof-20261013/fresh-recovery-formatted-wsl.*`.

Canonical LF formatting of all three changed Go test files, `git diff --check
HEAD`, and `go vet ./internal/generatedingress` passed with the same frozen
source inventory; vet took 47.407 seconds. The initial static check found a
formatting difference in the new helper and stopped before vet. That receipt
and the earlier passing test run are retained; `gofmt` repaired the difference
before final verification. Only tests and evidence change, so no new production
CLI build is claimed.

Pinned `pnpm --dir docs install --frozen-lockfile --offline` passed in 0.875
seconds with unchanged lockfile and documentation inputs. Documentation workflow,
build and accessibility checks are required before publication.

## Limits

Actual Docker execution requires hosted CI: the local desktop-linux engine pipe
is absent. The local WSL gate is not a Linux race or real Docker acceptance claim.
M3 remains incomplete; full-controller process restart and physical second-device
LAN qualification remain later gates. No production deployment or GitHub merge
is included.
