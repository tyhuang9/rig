# M4 generated migration authority

## Scope and status

This unit extends original deployment authority to generated migration
containers and durable migration results. Its base is the image authority
draft at `db2b0c161635a899b2c4f303da1c8fddaf4dc557` (PR #193).
Implementation and local verification are complete at code commit
`07f2bce19ecb79a736f0b361b14f02ae3df6be4b`. Actual hosted Docker and race
acceptance remain pending; local results do not establish M4 completion.

External databases remain application-owned and configured through scoped
runtime secrets. Rig does not provision databases or manage application schemas.
Candidate lifecycle, route completion and cold-process recovery are separate
M4 boundaries.

## Required authority

The concrete deployment repository retains the original Claim, worker attempt,
effects scope, deployment pins, accepted plan and configuration identities.
Its protected plan and configuration stores must use the same database.
Command checks validate current binding, predecessor, policy, approval,
pending-start fence, runtime state and the exact attached Ready image.
The runtime aggregate must contain the exact accepted component names and
count; a remaining subset of Ready components is insufficient.

Only a successful, guarded Pending-to-Running transaction can grant a new
single-use invocation. An uncertain commit acknowledgement grants none.
Existing Running or Failed evidence cannot grant another migration. A guarded
observation of an exact Succeeded result must not execute the migration again.

The repository owns the canonical process observer. Public command operations
accept no command, environment-key list, path, image, network, inspection result
or exit-code input. The observer derives these from the exact protected,
historically accepted revision and selected configuration. A newer plan head
does not invalidate immutable acceptance facts by itself.

Selected secrets are staged using the deployment ID and original worker
attempt. Secret buffers are cleared, and diagnostic output contains finite
codes rather than process output or environment values.

## Required observations and uncertainty behavior

Before each mutation, the complete fixed request is prepared and then checked
against current authority. No SQL transaction spans staging, Docker execution,
inspection or waits.

Create requires canonical image and application-network attestation, followed
by confirmed absence at the deterministic container name. Start and removal
require fresh inspection of exact ownership and configuration.
Accepted observations also require a fresh authority and context check after
inspection. Invocation consumption verifies the private concrete permit, so
embedding the public interface does not grant authority or cause a panic.

Container observations must match exact identity, provenance, image, command,
working directory, entrypoint, user, network and hardening. The observer must
have witnessed an actual successful start. A trustworthy exit requires matching
bounded wait and canonical exited-container inspection results.
Required scalar fields must be present with their expected types; missing or
null exit codes cannot imply success. Duplicate fields and malformed evidence
must be refused. Legitimate Docker null collections are handled explicitly.

Confirmed absence requires a complete response from a directly observed
Docker process exit with status 1 identifying the exact queried resource. Transport,
cancellation, termination, truncated output or misleading missing-object text
cannot authorize absence. Uncertain removal and reconciliation errors retain
their causes.

Only private evidence from those operations can authorize a result write.
Success also requires confirmed container absence without an uncertain removal
result. A public error code, a fabricated invocation or a successful return
from an arbitrary runner cannot authorize success or observed failure.

Uncertain create or start acknowledgement ends positive execution. A later
inspection may establish ownership for cleanup, but cannot authorize another
start or prove a migration outcome. Uncertain result commits preserve the
actual durable state without compensating writes. Application database effects
are never presumed rolled back.

The existing RUN-08 wrapper interruption must remain after the production
runner returns and before durable result persistence. Reopening controller
state must preserve the external ledger and make zero further runner calls.

## Required cleanup lifetime

Cleanup derives from the original live effects scope before the first Docker
mutation. It captures the full Claim and binding, deployment and invocation
identity once. It cannot retarget, reacquire the scope, grant positive commands
or renew its fixed absolute 70-minute deadline.

The private command owner fixes its own absolute deadline immediately before
deriving the child. All command and database operations share that owner
deadline, which is no later than the child's fixed deadline. Cleanup never
recomputes or extends it. Positive commands retain original cancellation.
Only cleanup can use the bounded cancellation-independent context. The child
permits the same running
attempt, or that exact attempt waiting externally in its cancelling phase,
subject to fresh binding, fence, provenance and runtime checks.

Parent and outer scope release wait for child cleanup and active process
termination. Deadline expiry does not release authority while a process is
still running. Without durable container intent, this child does not grant
cold-process cleanup or recovery; that remains E6 work.

## Local verification

Verified code commit: `07f2bce19ecb79a736f0b361b14f02ae3df6be4b`.
The later evidence-document update changes no code, test or workflow bytes.
Logical commits preserve the primitive, private-authority, controller-adapter
and hosted-fixture units without rewriting history.

Both platform runs selected 275 top-level tests and 451 subtests across the
complete appconfig, deploymentplans, deploymentexecution, deployments,
generatedexecutor, generatedmigration, generatedruntime and generatedruntimestate
packages, plus the 15 controller-composition parents.

- Windows: 725 passed, one skipped, zero failed. The sole skip was
  `TestRecoverRejectsSymlinkedOrUnrecognizedCleanupTargets`, reporting
  `directory symlinks unavailable`.
- Native Linux: all 726 passed, no skips or failures. This includes the case
  unavailable on Windows.
- Supplemental: all 14 steps passed, covering exact immutable-provider
  discovery/execution on Windows and Linux, four-package live-tag compilation,
  affected-package and live-tag vet, hostd build, canonical generation,
  committed-blob gofmt and aggregate diff checks.

The toolchain was pinned to Go 1.26.7, with readonly modules, serial package
execution, `-buildvcs=false`, `-count=1` and `CGO_ENABLED=0`. Windows used
10-minute package limits; native Linux used cross-compiled test binaries under
WSL with five-minute limits and the pinned Node 24.19.0 binary where needed.
These runs did not include race instrumentation.

Evidence paths below are relative to
`temp/m3-lan-external-db-20261015/m4-generated-migration-authority-verification/`.
Harness receipts retain exact command arrays, required/discovered test names,
source hashes and raw-log hashes. Every selected named test must have one run
and one terminal event; missing tests or unexpected skips fail acceptance.

| Evidence | Result | SHA-256 |
| --- | --- | --- |
| `final-native/freeze.json` | Exact commit, 30 changed paths and supporting test files frozen | `7759e8f4232d4f4f1dd275edfa224d2f58cf58f622fc0da5fccd329d42d51ae8` |
| `final-windows-reviewed-07f2bce/receipt.json` | 725 passes, one explicit platform skip | `9e78c34aa0763a767d327fbcc899123ab13063b068454a6ba78f8b456e620672` |
| `final-native/linux/receipt.json` | All 726 pass; no skips | `7e8ffa6f366dbff38fca05de6cb24abd64ba8c9d2bee5309703e3a034e1163f4` |
| `final-supplemental-reviewed-07f2bce/receipt.json` | All 14 static/build/pure-integration steps pass | `d921695cec844d8a5924adcfc123f30efb48973b2be44d15172af2464e6ef55d` |

The executable harness entry points were `run_final_windows.py run`,
`run_final_linux.py freeze` / `run`, and `run_final_supplemental.py run`.
Windows and supplemental commands used the exact code commit above and the
same `final-native/freeze.json`; their output directories are listed in the
table. Canonical generation ran
`pwsh -NoProfile -File scripts/check-generation.ps1`.

### Review and earlier checkpoints

Independent security and aggregate integration reviews cleared the source.
Review found and corrected ambiguity in absence evidence, missing/null or
duplicate inspection fields, incomplete component sets, permit validation,
fresh authority checks, parent-closing cleanup, uncertain-removal proof and
physical image/network attestation. Passing early tests were not treated as
proof that these gaps were absent.

Final focused observer coverage passed 24 parents and 115 subtests, including
actual SQLite commit-then-error cases that preserve the landed result. Receipt
`phase2/security-final-repair-receipt.json` has SHA-256
`be03602734ad800b1fb7de989234316506a8d8a6f101e9feebaa24665fa0973c`.
The independent raw-log/source audit is
`phase2/security-final-root-audit.json`, SHA-256
`ace88eabd1a8429bfd95167cc6b0707b633cb32a0e17fcf95ce9a029b0a61a3d`.

The clean base passed four migration parents and a package build. Primitive
coverage passed 14 parents and 13 subtests. Controller composition passed
15 parents and 57 subtests; the final focused executor check passed eight
parents and 12 subtests. These are checkpoints within the complete final runs,
not additional unique coverage.

Failed intermediate logs remain retained. They include an immutable-trigger
fixture setup failure and cleanup-ordering issue, outdated executor fixture
pins/projections, and missing test imports. The last import-only repair was
verified by the reviewer against its previous hash. The subsequent final
platform runs cover the corrected source. No limits were increased to make
these checks pass.

## Hosted approved-resume follow-up

The two retained PR #195 hosted failure logs are:

- `m4-lifecycle-coordinator-verification/ci-followups-20261020/pr195-job-118910188889/raw.log`, SHA-256 `01d496d42ae2cb23bcc96dcbcc5c216e393d2bf3aeffdfcb72e9546d580da797`: the approval-and-uncertainty journey stopped at the v2 resumed-job wait with durable `failed` / `runtime_unavailable` status before its migration-runner assertion.
- `m4-lifecycle-coordinator-verification/ci-followups-20261020/pr195-job-118910188517/raw.log`, SHA-256 `50b7344a05c79e85ec21865be75bb575b689d15c2819c7d49cb5154b30ec86d2`: the authority-revocation journey stopped at `migration executor did not return the retained authority loss` before its runner-result and retained-state assertions.

Those logs do not establish a root cause, runner entry, Docker create/start,
or SQL execution. The draft therefore adds failure-only test diagnostics before
fixture teardown. They report only allowlisted status/code enums, boolean
sentinel checks, bounded counters, and timestamp-presence booleans. The
approval journey reports process-observer counters as unavailable unless that
observer was armed, so its zero snapshot cannot be read as evidence of no
physical entry. No production behavior, retry, timeout, or authority check is
changed by this follow-up.

Local verification of the diagnostics passed on 2026-10-21 using pinned Go
1.26.7: both default-tag regression parents passed on Windows and native Linux
with zero failures/skips. They check redaction/classification and failure-only
defer ordering before worker shutdown. Live-tag controller compilation and vet
passed on both targets; canonical generation and all three documentation
checks passed. Evidence is retained in
`temp/m3-lan-external-db-20261015/m4-generated-migration-authority-verification/resume-diagnostics-20261021T003952Z`.
Source hashes were unchanged throughout that run; this result paragraph was
added afterward. Independent observability review accepted the corrected
ordering and output boundaries. The two-second context bounds history/runtime
reads, while the existing job getter and snapshot locks remain outside it.
No live Docker rerun or production root cause is claimed by these local checks.

## Hosted acceptance still required

The migration workflow has two fixed Docker matrix entries: the existing
approval/RUN-08 journey and authority revocation before start. Both retain the
existing 45-minute job, 32-minute step and 31-minute Go limits and run the
immutable-provider parent. The workflow requires exactly one run/pass for each
required parent and rejects skipped or failed test evidence.

The revocation fixture resumes at the original attempt, observes actual
container creation, then changes the durable job attempt while the original
context remains live. It requires no subsequent start, wait or removal call,
no external database increment, unchanged deployment/runtime/component
snapshots and an unstarted owned container before explicit fixture teardown.
The existing RUN-08 interruption remains after production runner return and
before durable result persistence; reopening must perform zero further
migration runner calls and preserve the external ledger.

A Bash/fake-Go replay using actual jq 1.7.1 passed 34 selector cases: two valid
and 32 expected refusals. Receipt `live-workflow-contract/receipt.json` has
SHA-256 `3ebb0b5b230922ed3cc70cf220232bfd3b93070b23c91e6390d11f1263cfef66`.
The runtime selector contract passed 30 invocations, three matrices and
16 refusal/failure cases, including the added migration package. These
contract checks executed neither Go nor Docker. The final supplemental run
compiled the live fixtures but did not execute actual Docker.

Actual Docker execution, Linux race results and hosted review must be checked
against the published head. No live controller was activated for these checks.

## Rollback and remaining limits

Reverting code does not undo an application's schema migration. Preserve
immutable deployment and migration history, uncertain resources and actual
database effects. Do not reset a migration to Pending, restore an unbound
runner, or introduce automatic retry or down migrations.

Draft publication permits hosted verification. It does not establish M4
completion or authorize activation or a GitHub target merge.
