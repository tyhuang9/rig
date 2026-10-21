# M4 LAN first-build diagnostics

## Purpose

The hosted two-application LAN journey previously timed out while the first
deployment remained `running` / `applying` / `building`, with both runtime
components pending and no image-ready artifact. The retained diagnosis is
`temp/m3-lan-external-db-20261015/m4-generated-migration-authority-verification/hosted-lan-building-failure-diagnosis-20261021.md`.
Its source was old head `3da9b656`; the [hosted job](https://github.com/tyhuang9/rig/actions/runs/39504576865/job/118910187979)
has raw-log SHA-256 `dfac31d863c4f452cdcba6d5672fc8e0e6afa20c131dc98de3aa2c67c5e69720`.

This follow-up adds failure-only instrumentation to the live test. It does not
change controller behavior, Docker requests, retry rules, the 14-minute
deployment wait, or any journey assertion.

## Failure snapshot

The live fixture wraps the already injected `runtimeprocess.CommandRunner`.
The wrapper delegates the original request unchanged and records only these
fixed operation classes:

- `builder_setup`
- `buildx_build_load`
- `image_inspect`
- `other`

`builder_setup` is restricted to Buildx setup verbs (`create`, `inspect`,
`ls`, and `rm`). Generic Docker network and container operations, and a Buildx
build without `--load`, are recorded as `other`.

For each class the failure line includes bounded entered and returned counts,
an in-flight boolean, a capped completed-request duration, and a separate
snapshot-time in-flight duration. The in-flight duration is calculated from an
injected clock and retained starts. If more than 64 starts are live for one
class, the line says that in-flight duration is unknown until that class drains;
the counters still retain the exact in-flight boolean. The line also includes
an allowlisted return status and stdout/stderr truncation booleans. Wrapper
entry means the request reached its delegate. It is not evidence that Docker
started or completed a physical mutation.

The line marks command and worker observers as available only when their
callbacks are configured. Shared fixture callers without those observers report
unavailable; their default values are not observations. With an available
worker observer, `worker_returned=false` means the failure was observed before
that worker returned. Finite error classifications preserve termination failure
when it is joined with cancellation or deadline errors.

Artifact aggregates are emitted only after the selected job, deployment,
runtime state, ready release, accepted plan, component roster, and artifact
query all agree on one application, release, and plan revision. All rows are
validated before any aggregate is retained. The query is bounded to 64 rows
plus a truncation sentinel. It reports only counts by artifact state and a
maximum attempt number across immutable historical attempts for that exact
release and plan; it does not identify a current selected attempt. It never
logs component names, artifact IDs, release IDs, commands, arguments,
environments, paths, Docker output, or errors.

The diagnostic defer belongs to `lanTwoAppDeployJob`. It runs before the outer
test's worker, API, and database teardown defers. A successful deployment
marks the diagnostic complete and writes no success-path diagnostic.

## Local verification plan

Use the pinned Go 1.26.7 serial lane for:

```text
go test ./cmd/hostd -run '^TestLanTwoAppBuild' -count=1
go test -tags=live_docker -c -o <temporary-path> ./cmd/hostd
go vet ./cmd/hostd
```

The default-tag checks exercise concurrent counter updates, output redaction,
operation classification, migrated-database query scope and bound,
artifact-provenance refusal, failure-before-teardown ordering, and quiet
success. The live-tag command only compiles the modified Docker gate. The
journey itself remains a separate hosted-Docker check and is not claimed by
this document.

## Limits

The snapshot locates requests that reach the test wrapper and selected
artifact rows that have already committed. It cannot prove a physical Docker
start, report process output or build progress, or name the exact stage before
the runner when no command reaches the wrapper. A timeout increase or a
production change requires new evidence from the hosted rerun.

## Executed verification — 2026-10-21

At base `445dccfb0265fb139858cb41d85218fc8bbc08d3`, the final source passed
the pinned Go 1.26.7 serial verification with CGO disabled and readonly modules:

- Exact discovery found all seven diagnostic parent tests. Windows and native
  Linux each passed 26 cases with `-count=1`, with no failures or skips.
- Both targets passed `go test -tags=live_docker -c` and
  `go vet -tags=live_docker ./cmd/hostd`.
- `pwsh -NoProfile -File scripts/check-generation.ps1` passed.
- `pnpm --dir docs check:workflow`, `build`, and `check:accessibility` passed.
- Formatting and `git diff --check` passed. All repository source hashes were
  unchanged during verification.

The tests include joined termination errors and their aggregation order,
observer availability, unchanged context/request/result/error forwarding,
concurrent observations, bounded overflow, redaction, real migrated-database
artifact queries, provenance refusal, and failure/success diagnostic ordering.

The receipt is retained at
`temp/m3-lan-external-db-20261015/m4-lan-build-diagnostics-verification/20261021T012516Z/receipt.json`,
SHA-256 `c1aa7bc83d22eb5dd32e2f62c576f79d1a5a627201dd0e7558e5b25061bc5ada`.
Earlier failed and superseded attempts remain in the evidence directory.
Independent source review found the reported termination, availability,
redaction, and forwarding issues resolved. Its receipt SHA-256 is
`c9bef3d65063efc7d71bace5e210d8fc40da40bcf68f585305b0298738a2863f`.
Live Docker execution and race detection were not available locally. Hosted
Docker/race checks and hosted review remain separate gates. These results do
not establish the cause of the earlier LAN stall or complete M4 acceptance.
