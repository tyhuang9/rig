# M4 live fixture session lifetime

Status: locally verified fixture repair based on draft PR #226 at `46375ec`.
Hosted Docker acceptance is pending.

## Failure and ownership

Each live candidate session owns the controller-wide deployment effects lease
until `CandidateSession.Close`. Health handoff and route activation keep that
session open so callers can finish execution-backed assertions and obtain any
required published-history proof.

The gateway readiness and blue/green Docker fixtures begin another candidate
while a successful intermediate session still holds that lease. Their cleanup
callbacks run too late to unblock the next synchronous acquisition. This is a
concrete source-level lifetime conflict, consistent with the earlier 900-second
hosted failure. The captured log does not prove the exact blocked invocation or
that this is the only failure in either journey.

## Repair boundary

Only three test files change:

- Gateway readiness closes blue after its initial route, serving and observation
  checks, before loopback begins. It closes the successful baseline and replacement
  sessions before starting the next candidates.
- Blue/green lifecycle closes blue after its initial serving check and closes the
  other application's session after the isolation assertions, before beginning
  the next candidate.
- The existing sequential effects-scope test attempts a second acquisition under
  a bounded context while the first real lease is held. It retains the existing
  expired-scope refusal, successful reacquisition and original-claim checks.

Session-map entries and idempotent cleanup remain intact. Original serving,
isolation, safe-failure, retirement-boundary, drain and cleanup assertions remain
in place after the relevant closes. Neither live parent consumes published
history through its session; other fixtures that do so must read that proof before
closing. This repair does not change the closed-session history contract.

Production locking, acquisition retry behavior, candidate authority, accounting
and physical cleanup are unchanged. The real execution leave, reservation release
and effects-scope release still implement session closure. No manual lock-file
deletion or synthetic success callback is introduced.

## Verification scope

On 2026-10-25, Windows verification used the exact three-file source freeze:

| Check | Actual result |
|---|---|
| `go test -c -o <retained-binary> ./internal/generatedingress` | Passed; fresh binary compiled from this source |
| Binary `-test.list` with the three exact parent names | Exactly three distinct parents discovered |
| Binary `-test.v -test.count=1 -test.timeout=120s -test.run '^TestLiveGeneratedAuthorityAllowsSequentialEffectsScopes$'` | One parent ran and passed in 1.84 seconds; no skips |
| `go vet ./internal/generatedingress` | Passed |
| `pwsh -NoProfile -File scripts/check-generation.ps1` | Passed |
| `gofmt -l` on the three files | Raw Windows CRLF copies listed all three; exact LF-normalized copies listed none |
| `git diff --check` and source comparison | Passed; source bytes unchanged by verification |

Both live environment flags were unset. The two Docker parents were discovered
but not executed. No local Docker, race run or full test suite result is claimed.
The test's one-second second-acquisition context exercises refusal while a lease
is held; no production deadline or live-journey timeout changed.

The retained evidence packet is
`live-candidate-session-effects-lease-lifetime-verification-20261025T211156Z`
(index SHA-256 `ee0312f62c6d13e060ced2a8ae45d6e8a80070dde084b45c8b1994db875a436c`).
Independent root audit verified all 50 listed artifacts, the actual test output,
binary, discovery and three source hashes. Its record SHA-256 is
`5d5b1fb1fc6387ae2f1ab80e82f509cdb9d4051d66744b6589e0d83960115aaf`.
The source/security review found no remaining source blocker.

The unchanged parent PR #226 reproduced the two live timeouts in
[Docker job 121283785537](https://github.com/tyhuang9/rig/actions/runs/40231422430/job/121283785537):
readiness failed after 900.79 seconds and blue/green after 900.48 seconds at the
runtime adapter deadline. That job checked out synthetic merge `b390e189` and
does not include this repair. It also contains a separate generated-image
authority constructor failure; this repair does not claim to resolve that case.

The effects-scope control establishes the lock primitive used at the beginning of
candidate construction. It does not construct a complete Docker candidate. The
two live parents must establish the full session, serving, history and accounting
behavior on the hosted disposable Linux Docker runner.

These files have no build tags. Normal package compilation and exact discovery
are required; actual live execution additionally requires
`RIG_RUN_LIVE_GENERATED_RUNTIME=1` and the existing disposable-host preconditions.
Discovering or skipping a live test is not a passing Docker acceptance result.

## Rollback and remaining acceptance

This changes test fixtures only and requires no production rollout. Reverting it
restores their prior session lifetimes without changing runtime data or immutable
history. External databases remain application-owned and use scoped runtime
secrets. The separate retirement, accounting completion, process recovery and full
M4 gates remain open.
