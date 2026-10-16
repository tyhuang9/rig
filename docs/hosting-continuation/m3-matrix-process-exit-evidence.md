# M3 matrix process-exit preflight repair

## Purpose

Handle a process disappearing while the Linux matrix self-test reads its
`/proc/<pid>/stat` file. This branch starts at PR #171 source
`4e900a2f65c983a9e2807379ebd81201d0cd5cfe` and changes only the Python
matrix verifier and this evidence record.

## Observed failure

PR #171 gateway run `38824114259`, attempt 1, checked out `eb93151`, whose
tree exactly matches that source. Two jobs failed in the runner self-test:

| Partition | Job | Raw log SHA256 |
| --- | --- | --- |
| 04 | 116762545782 | `7eaed848670494954e6908aadbbaa798e583dec74b58bf6fdacfd9fc75c2e7f2` |
| 06 | 116762545879 | `da4e5e40180e49c4ab2d2df53f0de2b315ed0e7023d8fe87d937f74c06c1a750` |

Both completed the manifest contract check, then raised `ProcessLookupError`
with `ESRCH` from the read of a killed child's procfs status. The helper
previously caught only `FileNotFoundError`. The Go preflight controls and
Docker matrix cases did not run. The independent cleanup step passed in
both jobs. These failures establish no matrix-case acceptance.

## Repair and invariants

The process-status helper treats both `ENOENT` and `ESRCH` as process
disappearance. Other status-read errors still propagate. Live processes
still fail a zero-timeout check; zombies still count as ended. The
existing process-group kill, residual-process refusal, case roster, event
validation, deadlines and Docker cleanup gates are unchanged.

The existing self-test now deterministically checks both disappearance
errors, permission and I/O error propagation, and live/zombie status. Its
Linux subprocess cases still exercise a pipe-holding child, children with
detached output, and rejection of residual groups after successful exit.

## Actual local verification — 2026-10-16 UTC

- Before the repair, an injected `ESRCH` reproduced the uncaught exception.
- Windows Python 3.12: `python -B scripts/check-supported-rebind-matrix.py
  --check-contract` and `--self-test` both exited 0.
- WSL Linux: `python3 -B scripts/check-supported-rebind-matrix.py --self-test`
  exited 0, including the real process-group cases.
- `pwsh -NoProfile -File scripts/check-generation.ps1` exited 0 with Go
  1.26.7, readonly modules and CGO disabled; OpenAPI route/schema test passed.

The new self-test controls run on both platforms. Linux procfs behavior
and actual process-group termination are verified by the Linux execution.
No local Docker daemon was available. Hosted execution at the new source
revision, all 63 matrix cases and independent cleanup remain required.

## Rollback and limits

Reverting this patch restores the previous intermittent preflight failure.
There is no application runtime, schema, secret, database provisioning or
live-controller change. Existing PR #171 execution continues independently;
its successes remain tied to its original source revision. M3 is incomplete.
