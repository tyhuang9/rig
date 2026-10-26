# Relay fixture setup timing

Status: implementation and focused local verification passed. Hosted
confirmation of the setup timing remains required.

## Observed failure and source binding

[PR #246's PostgreSQL job](https://github.com/tyhuang9/rig/actions/runs/40316208316/job/121549334841)
passed its PostgreSQL integration tests, then failed
`TestPostgreSQLRelayOutageConvergesDurablyAcrossRelayAndControllerRestart`
after 20.09 seconds. Its closed diagnostic reported
`phase=relay_enrollment_create error_class=deadline`; repository remainder tests
were skipped. The captured source checkout was
`aaab1d1760a77051a796505c444fadfe41c98ecc`, matching published head
`3a750657ab749e296d2a2191439e611f5bfaea9d` and tree
`71eb8b9441068caff69d83c6861a1c59b3584643`. Raw-log SHA-256:
`d8306ebaa90c2a4d7e48d9bdfa181a4689b3eae664efc9cb437335de6b85e034`.

The shared setup deadline starts before PostgreSQL setup and migration, relay
store construction, and SQLite initialization. The enrollment error therefore
does not establish that enrollment itself consumed the budget. The existing
record neither allocates time among those steps nor proves a relay network
problem or data race.

This follow-up starts from [PR #251](https://github.com/tyhuang9/rig/pull/251)
at `5bd5bf49e6cc1d41009eb6f7aeb81060bbed4774`. The three affected source files
have identical Git blobs on that base and the tested PR #246 head:

| File | Baseline Git blob |
| --- | --- |
| `internal/autodeploy/relay_outage_integration_test.go` | `976202f280efaa159bcc00ffe9ab457ba06ffb4b` |
| `.github/workflows/github-deployment-ci.yml` | `f03bea47d9e196655188e74de860213cddd71a1c` |
| `scripts/check-relay-outage-diagnostic-projection_test.sh` | `4eab160f6342a42ea92b077e0f5223a4e24a5cd5` |

That establishes source correspondence for the observed diagnostic gap. It is
not an execution result for the new branch or the combined baseline.

## Bounded observation contract

The test emits an `entered` checkpoint immediately before each existing setup
call and a `returned` checkpoint immediately after it, before existing error
handling. Returned means the call returned; the existing failure record retains
error classification. Entered-only evidence survives a nested fatal test exit.

The fixed phases are `postgres_setup`, `relay_store_create`,
`controller_db_open`, `relay_enrollment_create`, `relay_enrollment_claim`,
`relay_enrollment_complete`, `controller_seed`, `autodeploy_configure`,
`server_start`, `supervisor_start`, `initial_sync`, and `first_fixture_stop`.

Each record contains only the closed phase and edge enums, cumulative
`elapsed_ms` from 0 through 20000, `elapsed_saturated`, and directly sampled
`context_state=active|deadline|canceled`. Saturation means the actual duration
exceeds 20 seconds; it is false for an exact 20-second sample. Saturated values
cannot allocate time beyond the cap. Context state is an observation and does
not alter the existing error classifier.

CI projects fully matched records only on the existing failure path. It rejects
unknown enums, out-of-range numbers, inconsistent saturation, reordered or
duplicate fields, and extra/private text. It does not infer test success or
validate a global event trace. Focused emitter controls verify sequence,
single emission per edge, returned errors and nested fatal behavior.

The 20-second setup and journey budgets, five-second initial synchronization
wait, operations, contexts, cleanup and failure behavior remain unchanged.
This change does not add recovery authority or production behavior.

## Verification and remaining acceptance

Before source edits, the fresh pinned Go 1.26.7 autodeploy binary compiled and
discovered 70 parent tests, including the opt-in integration test and all five
selected existing setup controls. Those five controls passed with
`-test.count=1 -test.v -test.timeout=2m` and an anchored exact-name selector.
The existing shell projection contract passed using the actual jq filter and
exact Git-LF copies of the unchanged script and workflow.

The baseline packet preserves two runner failures: an incorrect PowerShell
discovery counter, corrected using the retained discovery output, and passing
a Windows CRLF shell script directly to Bash, corrected using the exact Git-LF
projection. Neither required a source change or recompilation. The accepted
baseline receipt is `51697af1757a09a766a5957aecb4ac564c0bce590c0bf3af3208a7666164dca9`,
with source freeze `32172f3e65b2c05bd8b056748ff3b9c7b6071c5f85d4e2033107f3c535e723a0`.

The first final changed-source execution used source freeze v2. Review
strengthened its subprocess assertion before that execution: failure cases
must include the expected fixed fatal text and helper FAIL marker; success
must include its PASS marker and no fatal text. Freeze v1 was not executed.

Fresh compilation passed in 3.737 seconds. Discovery found 73 parent tests,
including all nine required names exactly once. Seven selected control parents
and 19 subtests (26 distinct names in total) passed in 0.112 seconds, with zero failures or skips, using
`-test.count=1 -test.v -test.timeout=3m` and an anchored exact-name selector.
Coverage includes zero/exact-cap/over-cap timing, independently sampled context
state, invalid enum/range rejection, existing sanitized failure/deadline
controls, and real subprocess success, returned error and nested fatal cases.

The actual jq shell contract passed in 10.354 seconds. Its four sections
accepted 37 valid projections and rejected 90 malformed or unrelated records;
the new checkpoint section accounts for 14 accepted and 22 rejected records.
The contract uses exact Git-LF projections of the frozen script and workflow.

The representative subprocess checks the entered/returned/fatal protocol;
source review checks the twelve setup calls and their order. Local child
capture has a ten-second timeout and rejects output larger than 64 KiB after
capture; it is not a streaming memory cap. The opt-in PostgreSQL/Docker parent
is discovered but not run locally.

The immutable packet is retained at
`temp/m3-lan-external-db-20261015/m4-lifecycle-coordinator-verification/relay-setup-timing-20261026T145349Z`:

| Artifact | SHA-256 |
| --- | --- |
| `source-freeze-v2.json` | `823feedd69ec7d395c9fde68dd1e8a184a5937f69f325afa7800c618ecc210b5` |
| `focused-execution-v2.json` | `1ae4324e815a8ab2e3964d2ab60864df7aa7043544a123cecaf910d905b8bb16` |
| `static-execution-v2.json` | `a1ac2ea640bf5d8e409a6b8612c9ea2fcf42b850bb8c8be0814a715507cb5750` |
| Fresh final binary | `6c37ea41ee850cfe57f11008ff0a81025d51ff9b45c8767d43552605816f5cb9` |

Read-only Git-LF formatting, scoped autodeploy vet, canonical generation,
diff checks and final source-stability checks passed. The baseline document is
excluded from the source freeze and has a separate documentation build and
delivery receipt.

The next hosted run must supply actual phase timings if setup fails again.
This observation change does not fix a demonstrated slow operation, establish
combined-source Docker or race acceptance, implement interrupted E6 recovery,
or complete M4. Application databases remain externally owned and configured
through scoped runtime secrets; no managed database or Neon work is included.

## Rollback

Revert this test, projection and documentation change through the normal PR
workflow to return to the prior diagnostic output. Persisted application or
controller state is unaffected. No live controller refresh or deployment is
part of this follow-up.
