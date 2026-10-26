# Complete an interrupted route handoff after cold restart

## Purpose

Finish the same durable deployment when a fresh controller finds that route activation, predecessor retirement and accounting handoff completed, but route completion did not commit. The target state is Draining with exact 042, 043 and both 044 records and no 045 completion receipt.

This work follows the configured-debt and legacy-Current reconstruction repairs. It must complete the existing deployment through an authenticated current worker claim while preserving immutable creator identity, route history and retained successor capacity.

## Required authority and failure behavior

- Require the exact published cold construction, its sealed immutable proof, and a fresh scoped Current worker claim. Durable receipts alone do not grant execution authority.
- Reobserve and reattest cold state through the configured adapters. Require the published route attestation while the deployment is Draining.
- Hold the exact restored successor roster, charges, token generations, epochs, limits and receipts through the terminal transaction and any ambiguous-commit readback. Release only the temporary hold; do not release configured debt or change accounting ownership.
- Keep internal locks bounded. Do not hold ledger, group or coordinator locks across SQL, Docker, admin observations or external effects.
- Commit route completion, deployment/job success and the canonical success event atomically. Preserve original creator and current worker lineage.
- Use the existing completion replay guard for the same live claim or ambiguous readback. A newly observed Succeeded deployment retains the executor's existing refusal behavior.
- Reject stale handles, changed authority, mismatched receipts and incomplete accounting evidence. Preserve existing warm execution and bootstrap behavior; only the recovered-completion entry requires a cold proof.
- Do not repeat predecessor retirement, container operations, raw capacity acquisition or capacity release.

## Verification plan and current status

The preserved baseline proves fresh cold admission and claim validation separately from the executor's expected warm-domain refusal. Those two observations do not establish combined cold-to-executor completion.

The implementation adds a positive recovered-completion path, refusal and lifetime checks, and exact readback-helper controls. The hosted Docker journey injects a transaction fault before 045, removes that fault, restarts the controller in a new process, recovers durable jobs, and uses the existing authenticated resume endpoint when the job is WaitingUser. It must establish a fresh claim, exact completion and unchanged capacity/history. This is a fault-and-restart boundary, not evidence of SIGKILL inside the completion transaction.

Hosted CI requires the new boundary by name and rejects a missing, skipped, failed or duplicate result. Hosted acceptance remains pending; no local Docker execution or M3/E6/M4 completion is claimed.

### Local implementation evidence — 2026-10-26

The final cold regression passed after fresh compilation and exact discovery. It invokes `CompleteRecovered` inside an authenticated worker callback after genuine cold reconstruction and recovery. It verifies exact completion, unchanged retained capacity and prior history, public same-live-claim replay, crossed-claim refusal, observed configuration drift with no SQL/history or stop/remove changes, and readback-helper refusal after its temporary hold closes.

Post-success controls use a bounded detached context synchronously inside the still-active worker callback, before admission cleanup and `Leave`. This does not prove replay after that lifetime ends. Direct readback-helper controls do not simulate an end-to-end lost commit acknowledgment.

| Check | Actual result |
| --- | --- |
| Final cold completion and refusal parent | Passed, 1.978 seconds; receipt SHA-256 `f679060b53c720fd00f193821018f1352a490c7b5005b95b3691fbb035e3bf59` |
| Completed Current, initial legacy Current and interrupted Draining compatibility | Three parents passed, 4.416 seconds |
| Existing completion and new capacity-hold controls | Ten parents and 24 children passed, 12.820 seconds |
| Warm executor refusal control | Passed, 1.564 seconds; this is not a warm recovery success |
| Ingress, executor and tagged hostd binaries | Fresh compilation and test discovery passed |
| Formatting, three scoped package vets and `scripts/check-generation.ps1` | Passed; canonical controller test reported 0.721 seconds |

The final test source SHA-256 is `154459b7e01f509190102836b4bfa8e94ae02fbb8fe78d5327d960bfea032683`. Unchanged production and control-test sources permit explicitly bound reuse of earlier passing checks; the changed cold regression was recompiled and rerun. Earlier fixture, oracle and capture failures remain recorded separately. The first failing run lacked complete retained output and is not accepted evidence.

### Recorded prerequisite evidence

The integrated baseline was checked at `dcfd294f47cc8fd6e53238333053bd0435600696` before implementation. Its 41 archived artifacts were independently checked against index SHA-256 `b5d7e1fd631ffb77921a1c45cba6679b933d4ed4718c52fc554fbaae4c670e09`.

| Check | Observed result | Limit |
| --- | --- | --- |
| Fresh ingress and executor test binaries | Compilation and exact test discovery passed | Local Windows binaries |
| Four ingress parents | Seven unique passing cases, including restored debt, fresh worker admission and completion lifetime controls | Does not execute the new completion path |
| Fresh-worker executor regression | Expected route reconciliation refusal after 1.56 seconds | Warm-domain regression proving the missing behavior |
| Formatting, scoped vet and canonical generation | Passed | Docker and race checks remain hosted |
| Exact hosted result gate | Complete seven-boundary result accepted; 32 missing, skipped, failed or duplicate parent/child results rejected | Synthetic JSON checks CI enforcement only |

The required hosted child is `TestLiveControllerGeneratedProcessKillRecovery/after_predecessor_handoff_before_completion`. Existing job and test time limits are unchanged. The documentation-only dependency at `3a7f6f63851fc59ff00e25d5c5eb46f5c1df29ef` was subsequently fast-forwarded locally; it changes no Go source.

## Scope and rollback

The change concerns recovery authority, completion, its executor integration and focused tests. Application-owned databases and scoped runtime secrets remain unchanged. It adds no managed database or Neon provisioning. No schema or immutable-history rewrite is planned.

Rollback must preserve all committed completion records and retained accounting. Reverting the new recovery path restores its previous refusal for an interrupted handoff; it must not recreate retirement or release authority from stored receipts.
