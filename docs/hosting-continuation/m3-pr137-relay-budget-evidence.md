# PR 137 relay fixture budget boundary

Recorded 2026-10-09. Local test-only follow-up to published
`1b3eb244c2f29d30b7c8b77faff5bcca995f66d3`. This change has not been published.
No actual PostgreSQL outcome for the changed fixture is claimed.

## Observed failures

The same relay outage test failed on both published attempts after the separate
PostgreSQL store integration step passed:

| Source / job | Observed result | Preserved log SHA-256 |
| --- | --- | --- |
| `488046b` / `113462428784` | FAIL, 20.24 seconds; fixture line 79, outage source push assertion | Original log retained in `temp/pr137-job-113462428784-attempt1.log` |
| `1b3eb24` / `113849128418` | FAIL, 20.09 seconds; fixture line 133, durable source assertion after lost ACK and controller reopen | `EDD9B01B8E649D8C9E8F139A857CFA1FBCBE053EE2A7CC4E0801E440D5030D9D` |

Both logs are under `C:/Users/huang/Documents/Projects/Rig/`. The existing safe
CI filter discloses failure locations, not arbitrary database errors or result
objects. The later attempt advanced through initial synchronization, outage
updates and deliberate ACK loss. Neither failure is an initial-sync failure.
The repository race step following this test was skipped on both failed jobs.

The fixture created its only 20-second context before PostgreSQL migrations and
fresh SQLite construction. It then reused that context throughout outage,
restarts and convergence. The PostgreSQL reopen closure also captured it.
Both failures coinciding with that total deadline at different stages support
budget exhaustion as an explanation, but do not establish the hidden error.
No particular migration duration or production fault is claimed.

## Change and invariants

The fixture now has two bounded phases:

1. A 20-second setup context covers database setup, enrollment, configuration,
   initial authenticated subscription synchronization and stopping the first
   fixture. Its error is checked before proceeding, so setup overrun cannot be
   hidden by allocating a fresh budget. The setup context is then canceled.
2. One 20-second journey context covers all outage source updates, the manual
   deployment, controller and relay restarts, lost-ACK replay, durable readback
   and final latest-head dispatch. Restart does not reset this deadline.

The existing PostgreSQL reopen helper accepts this journey context explicitly.
All existing five-second waits, exact generation/inbox/ACK/idempotency assertions,
isolated schema handling and cleanup remain. Production code, migrations,
workflow deadlines, error-output filtering and runtime authority are unchanged.
The change does not add retries or access an application-owned database.

## Local verification

The source was frozen at SHA-256
`0A8621067BDC9F6DF5BCC758BD8C7743B14A2A8073F432CF91F4E2F9AF2285EE` for
`internal/autodeploy/relay_outage_integration_test.go` before and after checks.
Actual local toolchain was **Go 1.27.0**, not the earlier session's Go 1.26.0;
hosted workflows remain pinned to Go 1.26.7. No toolchain was installed for this
change. Flags were `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -buildvcs=false -p=1`, and `CGO_ENABLED=0`.

The exact native selection was:

```text
go test -json -count=1 -timeout=3m -run '^(TestRelayOutageControllerOpenRetriesOnlyTransientSQLiteLocks|TestPostgreSQLRelayOutageConvergesDurablyAcrossRelayAndControllerRestart)$' ./internal/autodeploy
go vet ./internal/autodeploy
go test -c -o <recorded-linux-binary> ./internal/autodeploy
```

The last command used `GOOS=linux` and `GOARCH=amd64`. That binary was actually
executed in WSL with the same anchored selection, count one, verbose output and
three-minute package timeout. `RIG_RELAY_TEST_DATABASE_URL` was explicitly removed
from both executions to avoid using any ambient database credential.

| Check | Result | Output SHA-256 |
| --- | --- | --- |
| Native Windows | Existing SQLite-open helper: 1 parent and 3 subcases PASS; PostgreSQL parent exactly once SKIP; exit 0 | `1CC07049CB64B741484D9EFBFEF76C1029B058A57380070209614FF6F8B8E7DF` |
| Package vet and Linux compilation | Both exit 0 | Empty output; commands/exits recorded |
| Actual WSL Linux | Same helper 1 parent and 3 subcases PASS; PostgreSQL parent exactly once SKIP; exit 0 | `C177AB035630A60E02806B31D83C644EC0CFB29D604C629EA1C4F413A7450A24` |

Records are in `temp/pr137-relay-budget-checks-20261009/summary.json` and its
per-command logs. An initial restricted WSL permission operation ended with
`WSL/E_ACCESSDENIED`; this environmental failure is preserved. Elevated execution
resumed only the permission step and the already-compiled Linux binary. Native
tests, vet and compilation were not repeated. Source identity stayed unchanged.

These helper outcomes prove compilation, existing retry boundaries and platform
execution. They do **not** exercise the changed PostgreSQL journey or establish
Linux race acceptance. The local Docker Linux-engine endpoint is absent, and no
local PostgreSQL executable was found. The full repository suite was not rerun
for this single test-file change. Scoped security and independent final source
reviews found no blocker. Actual hosted acceptance is still required.

## Required acceptance and rollback

After separate publication approval, run the exact PostgreSQL outage parent
under the existing hosted race gate. It must produce its required PASS event,
exercise durable lost-ACK restart/convergence and permit the following repository
race step to run. A setup-deadline failure or another hidden assertion remains a
failed attempt requiring investigation; a skipped opt-in is not acceptance.

Reverting this test-only change restores the previous fixture timing behavior.
It does not mutate runtime data, change application database ownership or require
a production deployment. M3 remains incomplete.
