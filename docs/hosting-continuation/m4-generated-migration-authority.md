# M4 generated migration authority

## Scope and status

This unit extends original deployment authority to generated migration
containers and durable migration results. Its base is the image authority
draft at `db2b0c161635a899b2c4f303da1c8fddaf4dc557` (PR #193).
Implementation and acceptance are in progress; the requirements below do not
constitute completed verification.

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

## Baseline and verification in progress

The clean base passed the four existing `internal/generatedmigration` parent
tests and a package build on Windows with pinned Go 1.26.7,
`GOTOOLCHAIN=local`, `GOFLAGS="-mod=readonly -p=1 -buildvcs=false"` and
`CGO_ENABLED=0`.

Evidence paths below are relative to
`temp/m3-lan-external-db-20261015/m4-generated-migration-authority-verification/`.
Receipts record exact commands, raw-log hashes and source identity.

| Evidence | Result | Receipt SHA-256 |
| --- | --- | --- |
| `baseline/receipt.json` | Four parent tests and package build passed on the clean base | `0b51882af163c3683eb18af004b7a7b0183b19fcfa458651ee650b8b04d30d54` |
| `migration-ci-baseline/receipt.json` | Existing selector contract passed before the package addition | `57a485f1f940f5f5c3082261356db2d26b32b7008487d9e71c00dd1fcf7b5d5f` |
| `migration-ci-final/receipt.json` | Selector contract passed with generated migration included | `81278d18e21ee297551de86bb38c338b9232745078d11dbff62d27f3dfd3c3b5` |

The shared selector includes generated migration in Windows tests and Linux
race tests. Each selector-contract run exercised 30 invocations, three matrices
and 16 refusal/failure cases using the existing fake-Go harness. Those checks
verify selection and failure behavior; they do not execute Go or establish
race-test acceptance.

Implementation verification must include identity and same-database refusal,
historical protected-plan behavior, copied/consumed invocation refusal,
cancellation and scope release, lost acknowledgements, real commit followed by
lost acknowledgement, and refusal of fabricated outcome evidence. The existing
live migration approval and uncertainty journey remains mandatory. Additional
actual-Docker cases must prove resumed-attempt provenance and authority loss
between creation and start without an external database increment.

No new migration Docker or Linux race acceptance is recorded yet.

### Review and integration checkpoints

The initial private-observer checkpoint passed 10 focused parents and two
subtests, and its full deployment package passed 50 parents and 102 subtests.
Security review nevertheless found gaps in absence classification, required
inspection fields, physical checks before mutations, permit validation,
component membership and observation authorization. These are being repaired;
the initial passing tests do not establish delivery readiness.

The thin runner passed nine parents and three subtests. After correcting the
executor's durable fixture pins, eight selected executor parents and 12
subtests passed. A new regression verifies that an uncertain runtime-state read
preserves existing migration state and causes instead of permitting generic
terminalization. Full integration checks must follow the observer repairs.

| Checkpoint evidence | SHA-256 |
| --- | --- |
| Initial observer `phase2/receipt.json` | `aa26f1ff2263bf68d60ea714525ca8629a8be50f7d64d5edc0e5519c4c7c92f7` |
| Independent observer log/source audit `phase2/root-audit.json` | `a9c654a8818d13a74ea7aaac0695a81804dfa549320b7381a7775bb00080e98e` |
| Runner checkpoint and first executor compile failure `integration-attempt-01/receipt.json` | `27d36e13403da966e2c994c055f2c167d8bf2d614fcbb065aa18794ae3a18710` |
| Focused executor raw log `integration-attempt-05/generatedexecutor/tests.json` | `fce5e2bde8b46294e8962b60b9d1b636a901b6e7d6529197ba4a0fe18eadd859` |
| Independent executor audit `integration-attempt-05/generatedexecutor/root-audit.json` | `780dcf4ef6214cf5fd142a89eceb2db012e3c03ff9b947de37ae11c578489f48` |

Source review cleared the executor, thin runner and controller wiring, and the
two live-fixture files after four test-fixture fixes. The live fixture now uses
full IDs for retained-container cleanup exclusion, canonical absence evidence,
a context-liveness observation at executor return and snapshot copies that
preserve empty slices. Compilation and real Docker execution remain pending.

### Primitive checkpoint

Local commit `b3da2ff1b17989fe94de2132dc4a26397c1ee6d3` contains the exact-database
store checks, fixed cleanup child and migration transaction helpers. Its
selected Windows tests passed 14 parents and 13 child tests with Go 1.26.7
(`GOTOOLCHAIN=go1.26.7`, readonly modules, serial package execution, CGO disabled).
This is focused primitive coverage, not full-package or hosted acceptance.

The evidence receipt is `phase1/receipt.json`, SHA-256
`8818b76be59fd9134a5a7d263991bbf6ccb1e4e3dbe803d5a78ab6f794191703`.
An independent raw-log/source audit confirmed every selected parent ran and
passed exactly once, with no skips/failures, and all evidence hashes matched:
`phase1/root-audit.json`, SHA-256
`72705ce189ae41849362a955a8f26365deec7dcb14328bd69ecfda536b6d3ba1`.

Earlier guard test attempts are retained. A deliberate missing-provenance
fixture initially hit the immutable-identity trigger before reaching the guard;
its first attempt also exposed incorrect test cleanup ordering and was stopped.
The fixture was corrected using the existing corruption-test pattern, and
cleanup ordering was fixed before the passing runs. Later tests cover expiry
without renewal and an existing child working while only its parent closes.

Security review cleared this primitive scope. It does not establish the
phase-two owner's actual process join, full policy/runtime checks, or outcome
evidence.

### Hosted gate selection checkpoint

The migration workflow has separate fixed matrix entries for RUN-08 and
authority revocation before start. Each keeps the existing job, step and Go
timeouts, runs the immutable-provider parent and selected journey, and requires
exactly one run/pass per parent with no package skip or failure.

A Bash/fake-Go contract replay using actual jq 1.7.1 passed 34 cases: two valid
selections and 32 expected refusals, including missing/duplicate test events,
wrong packages, skipped/failed children, nonzero Go status and malformed output.
Receipt: `live-workflow-contract/receipt.json`, SHA-256
`3ebb0b5b230922ed3cc70cf220232bfd3b93070b23c91e6390d11f1263cfef66`.
This check executed neither Go nor Docker. The new live parent and actual
hosted acceptance remain pending.

## Rollback and remaining limits

Reverting code does not undo an application's schema migration. Preserve
immutable deployment and migration history, uncertain resources and actual
database effects. Do not reset a migration to Pending, restore an unbound
runner, or introduce automatic retry or down migrations.

Draft publication permits hosted verification. It does not establish M4
completion or authorize activation or a GitHub target merge.
