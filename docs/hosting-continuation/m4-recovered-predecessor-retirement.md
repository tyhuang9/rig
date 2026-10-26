# Recover interrupted predecessor retirement

Status: implemented from [PR #258](https://github.com/tyhuang9/rig/pull/258), with focused local verification. Hosted Docker and Linux race acceptance remain pending. M4 remains incomplete.

## Required behavior

A controller can lose its process after committing the successor route but before finishing removal of the predecessor containers. The successor may already be serving, while some predecessor containers still run and others have already been removed. A restarted controller must account for both sets of resources and finish the exact deployment without using historical receipts as permission for new container operations.

This recovery covers a deployment in `Draining` with exact activation evidence (042), no handoff or completion (044/045), and one of these retirement states (043):

- No component absence records.
- A strict subset of the exact component absence records.
- Every component absence record, but no aggregate retirement receipt.

Complete retirement already enters the [recovered accounting handoff](m4-recovered-accounting-handoff.md) path. Recovery before a complete route activation remains separate.

## Authority and effects

Cold construction must restore the exact successor and predecessor capacity debts. A fresh worker must establish current scoped authority, immutable activation provenance, the current deployment head, the served successor route and exact component identities before attempting retirement. The implementation must retain both complete accounting rosters through the operation; it must not release and reacquire capacity as a shortcut between retirement and handoff.

Fresh physical observations determine the only permitted next step:

| Predecessor observation | Permitted action |
| --- | --- |
| Exact identity, running | Stop, then reobserve before removal |
| Exact identity, stopped and removable | Remove, then reobserve absence |
| Absent | Record or verify its exact absence record |
| Unknown, replaced, non-removable or contradictory | Refuse and retain capacity |

A component with an existing absence record must still be observed absent. Reappearance is a contradiction, not permission to remove the newly observed container. Previously absent components must not receive repeated stop or remove commands.

Before a physical mutation, require a healthy, running and correctly configured successor under the existing route checks. Health-independent reconstruction of capacity debt does not weaken this mutation requirement. Recheck worker authority and exact resource identity at every effect and durable-write boundary.

The original drain duration is not saved in the activation receipt. Recovery must conservatively wait the full existing supported maximum of 30 seconds before its first physical command. This may delay recovery longer than the original deployment requested; skipping the drain is unsafe. Cancellation during the wait must pause recovery without mutation. Verification must control this wait rather than sleep through it.

Preserve the existing stop/remove failure semantics. An error pauses the invocation; uncertain process termination retains the existing refusal state. A later cold attempt requires new physical evidence. Do not retry an ambiguous command in the same invocation or infer its success from a receipt alone.

## Retirement to completion

After every predecessor is freshly observed absent, write the aggregate retirement receipt. The original cold snapshot is now stale. Under the still-held exact rosters and current claim, establish a fresh proof of the post-retirement state before entering the existing accounting handoff and completion paths.

No coordinator or gateway mutex may span Docker execution or a SQL commit. Releasing an operation hold must not release its capacity debt. Only the verified accounting handoff may release predecessor debt; successor debt remains reserved.

This change must preserve immutable deployment and receipt history. It introduces no managed databases or database provisioning. External databases remain application-owned and configured through scoped runtime secrets.

## Observed baseline — 2026-10-26

On base `6380ee4e222bd13c5c2bd4bb64339d32b7a1596a`, the authentic regression `TestGeneratedDomainRestartAccountingActivePredecessorRecoversRetirementWithoutSecondShare` produced real activation with a completed lifecycle binding, retained both running container sets, closed the old handles and attempted cold construction with insufficient spare capacity.

The qualified baseline compiled with pinned Go 1.26.7 and discovered the exact expected test. It failed in 1.119 seconds at `cold_construction/cold_group_reconstruction`. Before reporting that expected failure, the test checked the exact durable prefix, unchanged SQL/protected history, unchanged stop/remove counts and zero additional capacity probes. This is an observed recovery gap, not a passing implementation result.

The refusal occurs earlier than the originally anticipated capacity check: lifecycle reconstruction requires historical predecessor groups, while the authentic activated replacement leaves those components draining. The repair must correlate that transition with exact activation and lifecycle evidence rather than discard lifecycle history or broadly relax ownership checks.

| Baseline evidence | SHA-256 |
| --- | --- |
| Frozen regression source | `5adccc0417130266c4510ecc30e1e76bec48c8aebc83d6253a153223116f5434` |
| Execution receipt | `df852bb18da62c69dbb31fdba043342bd85959903f4009b9e110e069e4f8b50a` |
| Expected failure output | `8a7abc982fcb732b1c7a5b01a7f4773a198405b4b711f828e04dc93088c45222` |
| Independent 12-artifact audit | `c05ad5dd3e07c1cda85725f501877b831e4ed629ab93133169f635e9f39a9c0a` |

An earlier baseline exited before its preservation checks and is retained as limited evidence. An unexecuted alternate fixture is excluded. The qualified execution receipt omitted its command array because of a capture-script scope error; the audit binds the frozen runner containing the exact executable, arguments and working directory. The original records are preserved.

## Local verification — 2026-10-26

The corrected source manifest is `c8735b378132775cedfce9499098037898cef365395e48453c7855d69ed3d9a3`. The final focused compilation with Go 1.26.7 passed in 12.867 seconds. Exact discovery found all six selected parents, and execution passed in 10.745 seconds. Its raw output hash is `89b7a0275c67ad86dfd6ce233fa6bfc5957820a0e3bef829f99d46d7c089003f`.

The accepted execution union contains 54 unique passing cases: 15 parents and 39 children, with 86 raw pass events because isolated child processes report some cases more than once. No accepted case failed or skipped. The union binds focused execution on the corrected source and explicitly scoped reuse of unchanged controls; it is not a single whole-repository test run.

- Authentic cold-first recovery succeeds with a running, stopped or already-absent predecessor and no component retirement receipt. The original controller handles are closed before reconstruction and a fresh worker claim performs recovery.
- A genuine two-component interrupted retirement preserves the first component's existing record, does not repeat its effects, retires the remaining component and completes 043/044/045 without acquiring another capacity share.
- Focused controls cover complete-roster hold atomicity, concurrent recovery refusal, crossed or duplicate records, foreign and reappearing containers, cancellation, fresh checks after the drain wait, command deduplication and uncertain-termination debt retention.
- Existing complete-retirement handoff and completion controls still pass. Source review found and corrected a first-deployment regression: a valid activation with no predecessor now defers to the established zero-roster validation. Its positive control exercises that evidence consumer, with four malformed or nonzero negative cases; it does not claim a new end-to-end first-deployment restart test.
- Formatting, relevant vet checks, canonical generation verification and `git diff --check` passed. Tagged hostd compilation and discovery verified the live test is present; only its pure boundary control ran locally.

Source and security review covered the exact recovery authority, physical effects, continuous accounting holds and the compatibility correction. Earlier failed development attempts and the superseded pre-correction packet are retained separately; they are excluded from final acceptance.

## Hosted verification still required

The required hosted case `TestLiveControllerGeneratedProcessKillRecovery/before_drain_stop` already exercises the crash before the first retirement command. It now runs first so the separate pre-activation recovery gap cannot prevent this case from executing. All other cases retain their relative order; the required workflow roster and time limits are unchanged. Its actual Docker result is required; local simulated Docker behavior cannot establish hosted acceptance.

## Rollback and limits

Reverting this recovery path may restore refusal after activation. Preserve existing receipts and retained capacity evidence; do not delete history, fabricate completion or repeat effects to make the deployment appear successful. No live-controller refresh or deployment is part of this branch's delivery.
