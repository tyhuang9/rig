# M4 rebind baseline deployment diagnostics

Recorded 2026-10-21. Base: draft PR #203 at
`44570972f612e06330c448ae4703def63f890e90`.

## Observed failure and scope

PR #194's hosted rebind partitions 02 and 07 each failed while constructing the
shared two-application baseline. The original-role-revocation case in partition
07 never reached its controller child or revocation operation. Its artifact
records exit 1 after 49.844 seconds, with neither wrapper timeout nor a residual
process group. Three preceding partition cases passed.

The existing observation reports `apply_failed`, `runtime_unavailable` and two
failed components. It does not identify the precise Docker operation. Cleanup
then retains resources whose exact legacy identity was not captured because the
baseline did not finish. That secondary refusal remains intact.

This change adds test-only diagnostic observation to the shared baseline fixture.
It does not repair runtime behavior, relax a test assertion, retry an operation,
change production logging or alter resource cleanup authority.

## Observation contract

- The production composition retains its exact `*runtimeprocess.ExecRunner`.
  The fixture worker wraps only the existing jobs executor/reporter seam.
- Fixed phases distinguish baseline A deployment, baseline B deployment and
  legacy identity capture. Two fixed observation slots privately correlate
  job/application/attempt identities. Conflicting identities become ambiguous;
  an unexpected third deployment cannot replace a recorded observation.
- Execution and reporting delegate exactly once with unchanged inputs and return
  values. Nil context/reporter behavior and original delegate/reporter panics are
  preserved. Only accepted progress updates advance the observed job phase.
- Error/context classification happens outside the observer mutex. A diagnostic
  classification panic discards its payload and produces fixed unavailable
  fields, preserving the executor's original result and error.
- Retained diagnostic state contains only allowlisted labels/codes and Boolean
  flags. It retains no error object, message, checkpoint, output, command,
  environment value or path. Correlation identities are never printed.
- A marked fixture failure emits its snapshot after the existing worker-stop
  step, including when the unchanged ten-second budget expires. Missing return
  evidence stays unavailable. Successful setup emits no diagnostic line.

The accepted executor phase can still be only `apply_runtime`, and a lower layer
may already have discarded its original cause. Unknown fields remain unknown;
this diagnostic must not be presented as an exact Docker operation trace.

## Acceptance status

The corrected source passed independent bounded privacy/correctness review.
Successor review SHA256:
`ce046acc2cf116bb80ba5a20ab220044635014906372085277b2d92254cf3787`.
The earlier HOLD remains preserved and is closed only by this reviewed successor.

The pre-format source checkpoint SHA256 is:
`b339b37c74a6792b86b503887b335f45ffbd5d864fae9050773b563c00d43045`.
Pinned gofmt produced successor bytes in both owned files. Final fixture SHA256:
`f5a9f578dcfaf35c2fa2759948a231458320dc0fb1baf5121b8a67f96c439dae`;
diagnostic SHA256:
`d52f247fd57d4c0d65b7e60b1ea12a6b695e9bbc96e0302dca941ab007011641`.

Pinned Go 1.26.7 checks passed on Windows and native Linux: exact discovery of
11 parents, 11 parent passes and 23 subcase passes on each system, zero failure
or skip. Seven parents are new diagnostic tests; four preserve existing behavior.
Both systems passed scoped tagged vet and hostd builds; tagged compilation and
the canonical generation check also passed. No full repository suite was run.

The first untagged listing discovered only ten expected parents because the
existing runner-pointer test requires `live_docker`. That incomplete discovery
is preserved; acceptance uses the corrected tagged recipe.

The complete 1,021-entry source manifest was captured after the Windows test,
vet and build, before the Linux and later static checks. Its SHA256 is
`5f0a4ecf733e4a3feabaf9b643d0327dce5e112145a30457b3ffebbaf7387632`.
Earlier owned-file hashes and normalized comparison of all other tracked
Go/module files with the exact base support source attribution. This is not a
claim that a complete before/after manifest surrounded the initial Windows run.

The actual hosted Docker diagnostic remains pending. Local passes do not
establish that the underlying deployment failure is fixed. Documentation
workflow, production build and accessibility checks passed with the source
manifest unchanged.

Evidence is retained under
`temp/m3-lan-external-db-20261015/m4-rebind-baseline-diagnostics-verification/`.
The initial source checkpoint remains at its recorded nested evidence location;
it was not moved or overwritten. Hosted partition-07 raw log SHA256 is
`3144ef13d848ce8762d10eed203b92a847aefa4eed4a222876c07d1b188293b2`;
artifact archive SHA256 is
`a58880e23f90082dd79120d70f610ee1b94d2f51a9505cc87e954cd8d8a1531d`.

## Rollback and remaining work

Revert the two fixture/test files and this document together. There is no schema,
application-history or live runtime state to roll back. Actual hosted evidence
must identify the remaining baseline failure before selecting a runtime repair.
