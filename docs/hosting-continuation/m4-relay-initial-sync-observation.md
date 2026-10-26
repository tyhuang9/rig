# M4 relay initial subscription observation

## Purpose

Retain enough bounded test evidence to distinguish the wait condition and
connection progress when initial authenticated subscription synchronization
fails. This follow-up starts from [PR #238](https://github.com/tyhuang9/rig/pull/238)
at `95ee19fafc36c3cc1fc0c8e505815351624e5e97`.

## Observed failure

The [PR #236 relay job](https://github.com/tyhuang9/rig/actions/runs/40256074106/job/121358077639)
failed `TestPostgreSQLRelayOutageConvergesDurablyAcrossRelayAndControllerRestart`
after 20.08 seconds. Its tested synthetic commit was
`cbae430bb03734fa038b83cec6995077590ddb7e`, with the same tree as the draft head.
Raw log SHA-256:
`039da2164e4f728b13799f9504e022a789e0a46a87c7cd47384ffa360e25cea5`.

The final observation reported one active SQLite subscription, one sync head,
no inflight sync set, and zero PostgreSQL relay sessions, leases, sync commands
and current subscriptions. The supervisor was connecting with an empty outcome
and was not paused. These counts are a snapshot; they do not establish that no
earlier connection or session existed.

The original projection did not identify which select arm fired or retain
connection progress. The workflow deleted its temporary Go JSON stream, and
the fixture did not configure the existing observers. Elapsed test time alone
does not distinguish the initial-sync timer from the setup context deadline.

The [PR #238 relay job](https://github.com/tyhuang9/rig/actions/runs/40258708625/job/121366115077)
failed earlier in enrollment setup after 20.26 seconds, at the enrollment
helper's caller on line 62. Its tested synthetic commit was
`ebbb6409474d479a9a0518be7be5d68159716572`, with the same tree as PR #238.
Raw log SHA-256:
`d99900872a8530e3c3e94ff8aa4b3c436a21e89f2cdd5e06771859da8163f89c`.
The retained location cannot distinguish enrollment creation, claim or
completion, and does not prove a deadline caused the error.

## Observation contract

Use the existing transport and supervisor observer APIs in the test fixture.
Record the selected wait condition directly, capped transport-stage counters
and milestone times, and finite supervisor fields with its observer-drop count.
Capture this observation before running the existing diagnostic count queries.

Transport callbacks are synchronous milestones. Supervisor callbacks are
asynchronous and may be dropped; their delivery time is not a transition time,
and their observations do not prove a complete event sequence. Unknown values
must not pass through as arbitrary strings.

The enrollment failure sites reuse the existing closed setup-error formatter
with distinct creation, claim and completion phases. The existing error
classifier remains unchanged. A class derived from the context's final state
does not establish the initiating cause of a database error.

The workflow emits only the fixed projection. It must not expose raw transport
errors, URLs, database connection strings, identifiers, credentials, frames,
SQL values or the complete test-output stream.

The five-second initial-sync timer, twenty-second setup context, transport
limits, retries, production code, cleanup and acceptance assertions remain
unchanged. This observation does not itself repair or establish the cause of
the hosted failure.

## Verification and remaining evidence

Verification covers deterministic recorder and formatter controls, both wait
conditions, finite values, numeric boundaries, unknown input, observer drops,
and the actual workflow projection's acceptance and rejection cases. Package
compilation and exact parent discovery must use the final source tree.

Hosted PostgreSQL and race verification must still execute the real journey.
A local projection test does not establish network convergence, cold Docker
startup, recovery or full M4 acceptance. External databases remain owned by
the application; this change adds no database provisioning.

## Rollback

Revert the test observation and matching workflow projection together while
preserving the recorded evidence and commit history. There is no runtime or
schema migration to roll back.
