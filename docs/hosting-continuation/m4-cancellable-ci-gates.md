# M4 cancellable CI summary jobs

## Purpose and observed failure

Four CI summary jobs used `always()`, which can keep them queued after a workflow
has been cancelled. This change is based on PR #232 at
`bd0503f34ccca60ae79ab713d3fc3e2de8532b28`.

On 2026-10-26, [run 40240530828](https://github.com/tyhuang9/rig/actions/runs/40240530828)
had 38 completed, cancelled test jobs and one queued summary job,
`Generated runtime race verification` (job `121330568495`), after normal
cancellation. The captured job-state JSON SHA-256 is
`dddbdd23f2815715a9e9039b6b2874f71354b785f5d3be010013b49dd3f0a064`.
Eleven other obsolete runs had the same terminal-tests/queued-summary pattern.
Their existing cancellations were completed with GitHub's documented
force-cancel endpoint after checking every job again. All twelve runs were
confirmed cancelled at 00:32:38 UTC; none is counted as passed acceptance.

## Change and failure behavior

The aggregate jobs in generated-runtime lifecycle, GitHub deployment, hosting
gateway v2, and Windows controller workflows now use `${{ !cancelled() }}`.
The hosting workflow's existing contract checker expects the new condition.

For a workflow that has not been cancelled, the status function allows each
summary to run even when a required job failed or was skipped. Its unchanged
strict success assertions then fail. For a cancelled workflow, the summary
condition is false. This follows GitHub's
[status-function semantics](https://docs.github.com/en/actions/reference/workflows-and-actions/expressions#status-check-functions)
and [cancellation behavior](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-cancellation).

All cleanup and upload conditions, dependency lists, matrices, assertions,
commands, timeouts, and check names retain their prior contents. Runtime code,
database ownership, deployment behavior, and secrets handling are unchanged.

## Verification and limits

Local verification records accompany this change: exact five-line source
comparison, the supported-rebind contract and self-test, whitespace checks,
and canonical generation verification. The canonical check executes Go-backed
OpenAPI generation and route-contract checks. Documentation verification uses
the frozen offline install, workflow check, build, and accessibility check.

Actual hosted cancellation of these changed conditions remains unverified.
Hosted follow-up must establish both that a normally failed required job still
fails its summary and that cancellation leaves no queued summary. Passing local
contracts does not establish Docker, cold-start recovery, or M4 acceptance.

## Rollback

Revert the four workflow conditions and matching contract expectation together.
No runtime configuration, data migration, or deployment is involved.
