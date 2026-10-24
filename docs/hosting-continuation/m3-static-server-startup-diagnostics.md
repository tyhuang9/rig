# M3 generated static-server startup diagnostics

Status: diagnostic candidate; the observed hosted failure remains unresolved.
This independent branch starts from PR #160 at
`950944edc9e028ce407060e4e726521876ae1c14`. The affected test source is identical
at that base and PR #161's observed `5ecefd7` candidate.

## Observed failure

[Generated-runtime race job 116412989354](https://github.com/tyhuang9/rig/actions/runs/38732307230/job/116412989354)
failed `TestManagedStaticServerDoesNotFollowSymlinksOutsideRoot` after 5.03s
with connection refused during readiness. The package took 112.62s. The test
had successfully started Node but never reached its symlink response assertions.
Actual checkout `242ad5b62cf0195715158df72e88b68655c2e698` matched the candidate
tree `53945a0b7729f878a2a459268f9b9e8582de0e99`.

Raw log SHA-256:
`59815fca4e949dadb1357e225a6f82e8a9fcaa442b7fc4dc160e35b0d209c8a0`.
The log and job metadata are retained in workspace wrapper
`temp/m3-lan-external-db-20261015/pr161-successor-fixture-ci-evidence/116412989354/`.

The old test discarded stdout/stderr and observed child completion only during
cleanup. Consequently, that evidence cannot distinguish an exited child from
delayed readiness. A port collision is a hypothesis, not an established cause.
The result does not establish a production confinement defect or disclosure.

## Diagnostic scope and invariants

- Attach an 8 KiB, synchronized output collector before starting the child.
  Report a closed failure category, captured-byte count, truncation flag,
  SHA-256 of the captured prefix, and an exit code only when known for an exited
  child. Do not print raw child output, paths or arbitrary error text.
- Observe completion before probes and while waiting between probes. One
  goroutine owns `Wait`; cleanup cancels and joins every successfully started
  child.
- Keep the existing five-second readiness budget, one-second HTTP timeout and
  25 ms probe cadence. Keep the production generated script and exact symlink
  fallback/disclosure assertions unchanged.
- Exercise readiness, completion, deadline boundaries and bounded concurrent
  output with deterministic controls that do not need Node, symlinks or sleeps.

The existing `runtime-packages` race batch runs this package, including the new
controls, through `scripts/check-generated-runtime-race.sh runtime-packages`.
The real Node/symlink test must pass in hosted CI; diagnostic controls alone
cannot establish that acceptance.

## Local environment limits

Before this change, root compiled and explicitly discovered the isolated
original test on Windows and Linux. Windows skipped because symlink permission
is unavailable; WSL skipped because Node.js is unavailable. Those attempts were
rejected as acceptance and retained in wrapper `generated-static-reproduction/`.
Windows raw log SHA-256:
`ce1bd64e0413a36df9a409e301410c86033bc245aa902aea6979de0f893f37ab`.
Linux raw log SHA-256:
`160316e7ce1bce5aaeadd240876f61befb231cfd7b003b827a82939a665649b9`.
Local race execution is also unavailable. No system permission or runtime was
installed or changed to hide those prerequisites.

## Rollback and remaining acceptance

This is test-only instrumentation. A reviewed revert restores the earlier
readiness loop and removes the additional failure evidence. It does not alter
application data, migrations, runtime authorization or deployment behavior.
The controller has not been activated or deployed. M3 remains incomplete,
including the supported-controller matrix and physical second-device checks.


## Verified local candidate

Both Windows and WSL Linux passed exactly two control parents and seven
subcases with uncached execution, no skips, and matching source hashes before
and after the run. Commands compile the package with
`go test -tags live_docker -c`, discover the exact recorded roster with
`-test.list`, and run through `go tool test2json` with
`-test.count=1 -test.timeout=10m`. These controls require no Node process,
symlink permission, real network, or sleeps.

Windows and Linux CLI builds and package vet passed. Canonical
`pwsh -NoProfile -File scripts/check-generation.ps1` passed. Whitespace and
formatter checks passed. Final docs workflow/build/accessibility receipts are
retained with publication evidence. Broad unrelated suites were not repeated.

Independent static integration review found no remaining blocker in the
three-file aggregate diff; this is not CodeRabbit review. It verified bounded
output, single ownership of child waiting, existing timing/security assertions,
and the correction that a still-running child cannot be reported as exit zero.
Formatting initially caught a malformed callback declaration; that was fixed
before any successful executable result was recorded.

Exact commands, source pins and raw logs are retained in wrapper
`temp/m3-lan-external-db-20261015/static-startup-verification/`.

- `native` controls SHA-256: `640b8e8cdee51f0b80bd24322a9bcacbb966ffcdac0419b53349efd3c3a10752`.

- `wsl` controls SHA-256: `d28820fd4db23d7f2682f33c274ea4811c54e7a44f83e5381510cc0f3ea35cf3`.


The generated-runtime hosted race job must still execute the real Node/symlink
test and these controls on the published revision. No local physical execution
or race pass is claimed, and the original startup cause remains undiagnosed.
