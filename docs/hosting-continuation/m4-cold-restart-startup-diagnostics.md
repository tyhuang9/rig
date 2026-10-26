# Cold restart startup diagnostics

Status: focused Windows execution and static checks passed on 2026-10-26.
Hosted startup acceptance remains pending.

## Observed failure

The migration, two-app LAN and application-owned TLS database jobs on
[PR #247](https://github.com/tyhuang9/rig/pull/247) reached a genuine fresh
controller process after durable setup and closure of the original owner. The
child exited before response zero, with no recognized startup diagnostic.

The exact tested checkout was
`ab347ec118fa0c7bcc9ee5cecfba00b461cf8338`, the synthetic merge for head
`3a57e8d81afaf683144f21d0e74fd26da5ec0dd9`.

| Hosted job | Observed result |
| --- | --- |
| [Migration approval and uncertainty](https://github.com/tyhuang9/rig/actions/runs/40309975363/job/121528401193) | Child exited before response zero; empty recognized diagnostic |
| [Two-app LAN routing](https://github.com/tyhuang9/rig/actions/runs/40309975300/job/121528400475) | Same observation |
| [Application-owned TLS database](https://github.com/tyhuang9/rig/actions/runs/40309975300/job/121528400797) | Same observation |

The parent captures bounded child stderr and parses the existing closed startup
record. Ordinary `t.Fatal` output goes to the test process's stdout. Nested
startup failures can therefore leave the recognized projection empty. These
logs do not establish which startup stage failed or whether accounting caused
the failure. Reported test elapsed times measure the parent, not child runtime.

## Scope and preserved behavior

This test-only change observes failure at the cold child's startup boundary. A
deferred observer covers nested `t.Fatal` calls using the existing finite stage
and reason vocabulary. Generated-domain construction errors use the existing
sanitized finite projection. Startup completion is recorded only after response
zero is written successfully.

The observer cannot grant authority or turn a failed test into a pass. It does
not recover panics, print raw errors, expose identifiers or paths, or change
production code. Startup order, response protocol, deadlines, worker cleanup,
Docker operations and protected history remain unchanged. Failures after
startup completion do not produce a startup record.

## Verification and remaining acceptance

Pinned Go 1.26.7 used `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`:

```text
go test -c -tags=live_docker -o <test-binary> ./cmd/hostd
<test-binary> -test.list=.
<test-binary> -test.v -test.run=^TestLiveHostdRebindClosedDiagnosticControls$ -test.timeout=5m
go vet -tags=live_docker ./cmd/hostd
pwsh -NoProfile -File scripts/check-generation.ps1
```

Fresh compilation, exact parent discovery and the focused run passed. The run
contains 236 distinct passing test names (one outer parent and 235 nested names),
with matching RUN/PASS sets and zero failures or skips. Actual nested `FailNow`
subprocesses prove one closed ordinary-stage record, one finite construction
record, and zero startup records from a failed child after completed startup.
Existing grammar, crossed/unknown projection refusal and privacy controls also
pass. Each control child retains its 30-second limit.

Scoped vet, canonical generation, diff checks and read-only formatting on exact
LF source projections passed. The final three-file source identities remained
stable. The final binary SHA-256 is
`dde3062a48958174705efb9c4a5cb1c48af66d7c2f9441615b1e10806eb37541`;
the final source manifest SHA-256 is
`45990e76d502a83e2bc3ebaa1a74f384b16dc66cf0a2e42612b578582ee2aa35`.

Review caught that the first completion control returned successfully, so it
did not exercise suppression after failure. That attempt is retained. The
corrected control fails after completion and independently requires zero records;
the final result comes from its freshly compiled successor binary. No failed or
superseded attempt is represented as final acceptance.

Actual Docker execution must identify the failing startup stage before a
production repair can be justified. Local diagnostic controls alone do not
establish successful restart, full lifecycle acceptance, or milestone completion.

## Rollback

Revert this test-only change to remove the additional startup observation. No
runtime state, schema or configuration migration is involved.
