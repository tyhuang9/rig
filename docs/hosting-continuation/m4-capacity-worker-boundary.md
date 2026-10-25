# M4 capacity-resume fixture worker boundary

Status: focused fixture checks passed on 2026-10-25. The first behavioral
successor passed both post-pause database reads and exposed a fixture drain-period
mismatch; the corrected successor passes the complete journey. Hosted Linux race
verification remains pending.

## Observed failure

PR #227's repository race job `121294246623` failed
`TestCapacityPausedDeploymentResumesWithRecordedPinsAndOneReplacement` at the
first deployment read after a worker pause. The returned error was
`generated domain database connection is unavailable`. The deployment slice was
nil with an error; it does not prove that the deployment row was missing.

The job used source merge `1968d237f5c4b885eb8b7d4cf154772604faed98` for head
`bb926095e9e373e78ceb0adcee8baa22c1f488d8`. Retained raw log SHA-256:
`a2c56ed83b2dde643762d071d4c1f7be1413ebf932f061259aa2e4edda014c0b`.
The relevant fixture, worker, repository and database source is unchanged in the
combined baseline at PR #230, `85866c35a27f9831fccae344684b3fad81414885`.

## Scope and failure model

The fixture currently cancels a looping worker when it observes the requested
persisted job status, before proving that the worker iteration has finished. A
subsequent worker database operation can overlap this cancellation. The sealed
database deliberately refuses to reconnect after losing its physical connection.

The retained log proves the reconnect refusal. It does not identify the
interrupted statement or prove that cancellation was the sole cause. The bounded
repair addresses the source-supported fixture lifetime race without changing
production worker behavior or database connection policy.

Only `internal/generatedexecutor/capacity_resume_integration_test.go` is in the
source scope. Use the fixture's existing scoped-admission callback to observe
authentic release completion and hold the worker before another iteration. After
observing the persisted target and completed release, cancel, unblock and join
the worker before inspecting deployment state. Preserve release errors and
bounded cleanup on failed assertions.

The fixture also needs an explicit short drain period. Its previous `Options{}`
selected the production 30-second drain period, while its existing completion
wait is five seconds. Receipt-owned completion now waits for that configured drain
before checking predecessor receipts, including an empty predecessor roster. Use
a one-millisecond test drain period, matching the existing sealed fixture. Keep
the production 30-second default and the fixture's five-second wait unchanged.

The existing acceptance assertions remain: two capacity pauses, three attempts,
immutable plan/configuration/release provenance, resistance to a changed current
configuration, one deployment, one ready release, one compile/candidate/route,
zero migration and eventual runtime success.

## Verification

Fresh retained Windows binaries compiled and discovered both exact parents:

| Package | Exact parent | Result |
| --- | --- | --- |
| generatedexecutor | `TestCapacityPausedDeploymentResumesWithRecordedPinsAndOneReplacement` | PASS, 1.47 seconds |
| database | `TestGeneratedDomainDatabasePoolCannotReconnectAfterBadConnection` | PASS, 0.43 seconds |

Both ran once, with no named child tests, failures or skips. Each binary used
`-test.v -test.run '^<exact-parent>$' -test.count=1 -test.timeout=2m`.
The second parent is unchanged and is the negative oracle that reconnect remains
forbidden. The two-minute runner timeout does not change the fixture's existing
five-second waits.

Formatter checking, `go vet ./internal/generatedexecutor ./internal/database`,
`scripts/check-generation.ps1` and `git diff --check` passed. The canonical script
also checks migration mirrors, OpenAPI output and its registered-route contract.
Independent review verified the admission-release ordering, error propagation,
fatal join timeout, supported positive drain period and unchanged production
policy. Release-error and failed-assertion cleanup branches were source-reviewed,
not separately executed.

The first retained behavioral run on 2026-10-25 completed the two pauses and their
deployment reads, then failed the existing five-second third-attempt wait. At
that boundary the job was `running`, runtime phase was `draining`, the component
was `active`, three capacity checks had occurred and one route input was
recorded. No timeout increase or production change was made. This later boundary
was traced to the configured 30-second drain above; the post-release worker
barrier occurs after execution and does not introduce that delay. The historical
failed run remains part of the evidence.

An earlier harness discovery error treated a single PowerShell output line as a
scalar; it ran no behavior. A separate failed metadata snapshot command created
an unintended duplicate test file at the worktree root. That untracked copy was
preserved outside the checkout before delivery, with its provenance recorded.
Explicit package builds/tests and canonical commands did not use that copy.
The original evidence was retained with an additive correction; successful tests
were not replayed solely for metadata or snapshot cleanup.

Evidence digests:

- Reviewed and executed source: `f65252613a4ae3edbbf6897660e4eb22408a55af208f65ea92a442156ca3ea7d`.
- Final combined record: `ebb0864f30de5c477dbffed99092a3be48e67f9b1fe74b1753fa93847b9a2a15`.
- Artifact index, 45 verified files: `6f55a047555fdf56a9ae2c3cce58708fbba6b38981e1589c8db913fa973f1bd8`.
- Independent raw-output/source audit: `d50e02702593403a13bb3b1e1d7635b92421e5a2bd4030daca4d589cf404bd89`.
- Source/security review: `2df7172ec08a688b36c71203490c522fca756bd6ce42e074a7024869ee340dea`.
- Preserved stray-snapshot provenance and scope correction: `9fa32da86eaa91eae2b09b4aff16b0244749733abced10a64061286e04f2acef`.

Documentation delivery also runs frozen offline installation, workflow checks,
production build and accessibility checks. The publication receipt binds their
actual results and logs.

Hosted Linux race verification remains required. A local passing successor would
show that the controlled fixture preserves retry assertions with cancellation
after release; it would not establish the exact historical interrupted SQL
statement, production recovery, E6 or full M4 acceptance.

## Rollback

This change affects tests only. Revert the fixture change if it masks a lifecycle
failure; keep the deployment/provenance assertions and database no-reconnect
negative test. Do not relax production database, authority or history safeguards
to make the test pass.
