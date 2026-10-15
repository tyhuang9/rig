# M3 relay writer terminal cause

## Observed failure and repair

[Repository race job 116552666837](https://github.com/tyhuang9/rig/actions/runs/38772790511/job/116552666837)
failed `TestWriterFailurePropagatesAndForcedCloseUnblocksWriter` on source
`4984042be056ae9ff4e5019f8b88f44e4f6443c1`. A sender observed the writer's closed
completion channel and returned a generic stopped error instead of the actual
write failure. The actual checkout tree matched that source. Raw log SHA-256:
`20ab0b8d7bbce1b84c223db12276b2d3c2a54a78b212f3eafd790576649b49b6`.

The sole writer stores its failure before completion. Senders read that cause
only after the completion channel closes, so consuming a one-shot notification
cannot erase it. Terminal draining resolves queued requests and clears their
encoded buffers. An atomic stopping flag coordinates late enqueue cleanup;
senders clear shared buffers only after the writer has finished. Production
completion channels remain buffered. A write result is classified using one
cancellation observation, preserving the same cause through notification and
draining even if cancellation arrives between them.

Sole writer ownership, outbound capacity, nonblocking queue-full behavior,
normal-close fallback, forced-close bounds and public error categories remain.
Authentication, protocol frames and persisted data are unchanged.

## Executed verification

Go 1.26.7, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, `CGO_ENABLED=0`.
Actual Windows and Linux amd64 test binaries were compiled and enumerated with
`-test.list=^(Test|Example|Fuzz)`. They ran through `go tool test2json` using
`-test.v=test2json -test.count=1 -test.timeout=10m`; Linux execution used WSL.

| Check | Windows | Linux |
| --- | --- | --- |
| Writer terminal regressions | 6 parents / 2 subcases passed | 6 parents / 2 subcases passed |
| Entire `internal/relay/wss` package | 56 parents / 90 subcases passed | 56 parents / 90 subcases passed |
| `go vet ./internal/relay/wss` | Passed | Passed |
| `go build ./cmd/rig-relay` with external output path | Passed | Passed |

Every discovered parent and emitted subcase ran and passed exactly once, with
no skipped or failed cases. The controls cover durable failure after consuming
the notification, queued failure and buffer clearing, cancellation arriving
between classification and publication, cancellation without a failure cause,
normal close, and the existing writer-failure/forced-close contract. The
classification boundary uses channels to position cancellation; no sleep or
production hook determines the ordering.

Two new regressions were run against the original production `session.go`.
Both failed their expected assertions: the durable cause was missing and a
queued request remained unresolved. The exact repaired bytes were restored.
Projection log SHA-256:
`e9b236779f0e3ae650da64a1c81e38e382cdac9d76462e8b588cd73bb5e2e286`.
Twenty initial unchanged-source Windows repetitions passed; those repetitions
did not disprove the hosted failure.

Canonical `pwsh -NoProfile -File scripts/check-generation.ps1`, formatting and
`git diff --check` passed. Independent code/concurrency and security reviews
found no blocking issue. Production file SHA-256:
`8954a2ac46029c125d980d12ae15763d2ce6fd08b07d639d6040202588dda44c`.

Final full-package raw log SHA-256 values:

- Windows: `a28299cc17d42d52e824cc0f0b44cad54e4131049a0a22cc5036a274f420448e`.
- Linux: `65b348da4fb00b54f5b5343a83b12a6355d32aada4c3f54ab21518ab38a460e0`.

Exact commands, inventories and source hashes are retained in wrapper
`temp/m3-lan-external-db-20261015/relay-terminal-ordering-verification/wss/`.

## Limits and rollback

Local execution did not use the race detector. Hosted Linux race validation is
required; these tests do not qualify Docker, physical LAN or M3 completion.
No live deployment or controller change was made. Reverting this repair
restores the prior failure-delivery and queue-cleanup behavior, including the
known race. No schema or data migration is needed.
