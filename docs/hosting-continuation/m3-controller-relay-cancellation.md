# M3 controller relay cancellation ordering

## Observed failure and repair

[Repository race job 116552666837](https://github.com/tyhuang9/rig/actions/runs/38772790511/job/116552666837)
failed `TestActiveSessionReplaysPendingControlsThroughSoleWriterAndClearsScratch`
on source `4984042be056ae9ff4e5019f8b88f44e4f6443c1`. A terminal read raced with
parent cancellation and returned `connection_closed` instead of the observed
cancellation marker. The actual checkout tree matched that source. Raw log
SHA-256: `20ab0b8d7bbce1b84c223db12276b2d3c2a54a78b212f3eafd790576649b49b6`.

The terminal classifier now preserves this order: recorded session expiry,
observed parent cancellation, then the original transport result. It is applied
to competing reader/writer and cancellation exits. An unrelated raw
`context.Canceled` does not acquire the observation marker. The supervisor still
requires its own reconciliation cause; explicit successful control actions,
authentication, replay, fencing, worker joins and scratch destruction retain
their existing contracts.

## Executed verification

Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`.
Compiled native Windows and Linux amd64 test binaries were enumerated with
`-test.list=^(Test|Example|Fuzz)`, then executed through `go tool test2json`
with `-test.v=test2json -test.count=1 -test.timeout=10m`. Linux execution used WSL.
All discovered parents and emitted subcases were reconciled with terminal
events; every run passed or was explicitly excluded below, exactly once.

| Check | Windows | Linux |
| --- | --- | --- |
| Replay and precedence regressions | 2 parents / 4 subcases passed | 2 parents / 4 subcases passed |
| Entire `internal/controllerrelay` package | 193 parents / 142 subcases passed | 195 parents / 144 subcases passed |
| `go vet ./internal/controllerrelay` | Passed | Passed |
| `go build ./cmd/hostd` with external output path | Passed | Passed |

The focused selector is
`^Test(ActiveSessionReplaysPendingControlsThroughSoleWriterAndClearsScratch|SessionTerminalFailurePrecedence)$`.
Precedence cases cover an uncanceled transport error, independent raw
cancellation, observed parent cancellation and expiry preceding cancellation.

Windows excludes the symlink-credential parent and two symlinked-parent
subcases because symlink privileges were unavailable, plus the POSIX-mode
parent because it intentionally skips Windows. These are not passing checks;
their Linux counterparts all passed. An initial verification-harness assertion
incorrectly rejected these known platform exclusions; the test process itself
passed. The initial static check also caught a discarded test cancellation
function. Its explicit cleanup was corrected and final verification rerun.

Removing only the observed-parent-cancellation classifier branch caused the
deterministic precedence regression to fail. Exact production bytes were
restored. Mutation log SHA-256:
`39b07c2fd9bbb695e8d0548348e65c43903a8c04b5c13a49b28bfb9aa8cf06bc`.
Initial unchanged-source Windows repetitions passed twenty times, which did
not disprove the hosted ordering failure.

Canonical `pwsh -NoProfile -File scripts/check-generation.ps1`, formatting and
`git diff --check` passed. Independent code/concurrency and security reviews
found no blocking issue. Production file SHA-256:
`4cde761958157ded7d6b2e6d238f1eaa66d445f1a8cb5d20193310214e6c4e06`.

Final full-package raw log SHA-256 values:

- Windows: `58733911694398173ce1838c34294913a9fa278fda902bb33f596cff1c91054d`.
- Linux: `832b33cb3f6be5f6cae5f94223143a1d16c5619bd5f79571a2951470feb7cec0`.

Exact commands, inventories and source hashes are retained in wrapper
`temp/m3-lan-external-db-20261015/relay-terminal-ordering-verification/controller-final/`.

## Limits and rollback

Local execution did not use the race detector; hosted Linux race validation is
required. No live controller, Docker recovery, physical LAN or M3 completion
claim follows from these package checks. No persisted schema, credentials or
configuration changes are introduced. Reverting this repair restores the prior
classification behavior and its known race; no data migration is needed.
