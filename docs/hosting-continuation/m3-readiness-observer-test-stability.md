# M3 readiness observer test stability

Date: 2026-10-13. Scope: make the relay readiness observer test deterministic
without changing production readiness or metric behavior.

## Cause and correction

The hosted PostgreSQL-and-race job failed only
`TestReadyObserverUsesClosedProbeAndCacheStates`. Its log records the three
observations `cached_success`, `probe_failure`, and `cached_failure`; the
earlier `probe_success` callback had not run when the test inspected its
slice.

`runReadyProbe` publishes the result, clears the in-flight probe, closes the
probe channel, and unlocks before it calls the observer. The `Ready` caller
can therefore return and issue a cache read before the probe callback is
scheduled. The observer contract supplies closed result and state codes; it
does not promise callback order across the probe goroutine and callers.

The test now uses a buffered notification channel and a one-second failure
deadline to wait for all four callbacks. It asserts the exact unordered
multiset of `(result, state)` pairs and two database probes. It also asserts
that neither observer field includes the database detail used by the failing
probe fixture.

## Boundaries

Only `internal/relay/store/readiness_test.go` changes production-test
coverage. `internal/relay/store/store.go` and `cmd/rig-relay/metrics.go` are
unchanged. The metrics consumer accepts known state codes as atomic counters
and has no ordering dependency.

## Verification

Before the correction, the focused test was invoked 100 times locally and
passed in 0.215 seconds. That bounded result does not contradict the hosted
scheduler-dependent failure.

The post-change checks used Go 1.26.7 from the pinned local toolchain with
`GOTOOLCHAIN=local` and
`GOFLAGS="-mod=readonly -p=1 -buildvcs=false"`.

| Check | Result | Receipt SHA-256 |
| --- | --- | --- |
| Focused observer test, 100 invocations | 100 RUN and 100 PASS events; no skip or failure | `1b72615caaa4424f6d317ce87487afbe7d15a8826353bff8d7cdd301d1f457f9` |
| Non-PostgreSQL readiness suite | All eight expected readiness parents passed once; package passed; no skip or failure | `20c3f42ffa058334c5821a538425cf4f8a8f4bb5954806f31b503c13466e6772` |

The receipts are retained under
`temp/m3-readiness-observer-test-20261013/`. Its full Go, SQL, and module
input manifest contains 887 files and has SHA-256
`809856fe2a6694f0a1765e24304dfdbdd1dae2c559c9caaee0f75c2fe27a057b`.
The exact `go.exe` has SHA-256
`5463fe58fa999d74420f00ee1b36d31c3da90a57ff204159f159859567ad61fc`.

The readiness suite ran these exact parents:

```text
TestReadyExecutesOnlySelectOne
TestReadyCollapsesErrorsAndHonorsCallerDeadline
TestNilStoreIsNotReady
TestReadyObserverUsesClosedProbeAndCacheStates
TestReadySingleflightTTLsAndFailedRefreshInvalidation
TestReadyStaleBoundCallerCancellationAndCloseCancellation
TestReadyCanceledCallerDoesNotCancelSharedProbe
TestReadyClockRollbackInvalidatesSuccessFailureAndStaleInFlight
```

An initial sandboxed WSL capability check failed with
`Wsl/E_ACCESSDENIED`. Its approved read-only retry found WSL Linux but no
Linux `go` or `gcc`; therefore no actual Linux race execution was available.
No cross-compile result is presented as a Linux race execution, and no
toolchain setup was attempted.
