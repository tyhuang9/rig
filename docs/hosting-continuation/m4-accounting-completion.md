# M4 route accounting and deployment completion

Status: development branch `feature/hosting-m4-accounting-completion`, based on
draft PR #224 at `09ecce7`. Immutable handoff persistence (Unit 2A, `43c777e`) and
private capacity ownership (Unit 2B, `f37fbb1`) and authenticated accounting handoff
(Unit 2C, `b862e10`) are implemented and checked locally. The purpose-specific
completion transaction writers (Unit 3A, `5b1f4bc`) and authenticated same-process
completion coordinator (Unit 3B, `ed7a5ce`) are also checked locally.
The executor wiring (Unit 3C, `8f5d11e`) has passed focused local verification.
Its immediate same-process path completes only through the retained authority;
uncertainty and missing authority still pause closed. Hosted acceptance is pending.

## Capacity ownership

A live route must retain its configured capacity reservation. Current available
memory and free disk report physical use; they do not reserve everything a running
container is permitted to consume. Another application's admission cannot see a
same-deployment tombstone in the private candidate ledger.

Therefore a successful handoff preserves the successor's original live raw
leases. Ownership moves from the candidate reservation through a transferring
state to a route-active owner. Candidate and alias leave operations detach their
references without releasing these leases. A future authenticated activation may
retire this route as its exact predecessor; generic cleanup cannot release it.

Both complete rosters are validated before changing any member. Validation binds
the ledger, group, logical tokens, charge epochs, original lease pointers, receipts
and physical states. Short group-lock intervals have deterministic ordering. No
SQL statement, gateway operation or raw lease release happens under those locks.
A completion transaction may remain open during the short in-memory validation.

The empty-predecessor case transfers successor ownership without releasing any
capacity. The full-predecessor case must release only predecessor reservations,
after fresh complete-roster absence, committed authorization and successful
gateway unlock. Unit 2B introduces no release authority by itself.

## Durable boundaries

Mirrored migration 044 and its purpose-specific repository persist immutable
handoff authorization and receipt rows. Typed inputs and SQL constraints bind the
exact activation, retirement, application, deployment and component rosters.
Exact replay preserves original timestamps. Persisted rows alone do not restore
process-local authority to release capacity or complete a deployment.

The authenticated handoff preserves this order:

1. Borrow the sealed handle and candidate lifetime; obtain both complete rosters.
2. Revalidate current authority, immutable receipts, serving successor and absent
   predecessor, excluding unresolved history and competing route operations.
3. Commit authorization while every successor and predecessor lease is charged.
4. Transfer the complete batch's private ownership with all leases still live.
5. Successfully unlock the gateway before releasing any predecessor lease.
6. Perform finite, authorized predecessor release once outside external and ledger
   locks; retain every successor lease even if the caller is then cancelled.
7. Reacquire the proof boundary and record or read back the immutable handoff
   receipt only after verifying the exact final ownership and physical state.

Uncertainty must retain charges. SQL receipts cannot substitute for missing
process-local proof after restart; that recovery remains separate E6 work.

## Completion persistence and executor boundary

Mirrored migration 045 retains an immutable completion receipt binding the exact
activation, handoff, successor, creator job/attempt and current job/attempt.
Unit 3A supplies separate runtime, deployment and job transaction writers. Runtime
mutation requires `draining`; main deployment mutation requires `waiting_health`;
the exact current job must be running and noncancelling. Terminal replay is a
separate, receipt-backed read path.

Runtime and main writers compare complete persisted activation and handoff values
with their typed input. Before its terminal CAS, the jobs writer freshly checks
the active head, succeeded runtime and exact active successor roster. It preflights
the per-attempt event budget and writes one canonical event for that same attempt.
The caller owns the transaction. Tests establish that runtime, main deployment,
completion receipt, job and event commit or roll back together.

Only an opaque `Complete(context.Context)` operation may finalize the deployment.
The coordinator derives the current sealed claim and attempt from its retained
handle. Every invocation opens a fresh transaction, including successful replay
and retry after a lost commit acknowledgment. It validates runtime and main state,
activation and handoff receipts, complete rosters, retained successor leases and
retired predecessor tombstones. The current-attempt job CAS, bounded canonical
event and completion receipt share that transaction.

Terminal replay uses a separate read-only guard and matches the exact job ID,
attempt and immutable completion receipt. It does not broaden the ordinary
retirement mutation guard. Private completion serialization and lifetime borrows
prevent concurrent aliases from duplicating the event or outliving their handle.

Cancellation that wins before the CAS rolls back all of those changes. An earlier
activation creator attempt remains immutable provenance and cannot replace the
current worker attempt. Successful completion never releases successor capacity.

The immediate same-process handle retains the exact claim used for activation,
so its successful current and creator attempts are equal. Advancing the job's
attempt makes that handle stale and must refuse. A genuine later-worker success
requires a separately authenticated recovery capability in E6. Distinct attempt
fields in the persistence schema do not establish that capability.

## Local evidence so far

| Checkpoint | Executed evidence | Limits |
| --- | --- | --- |
| Unit 2A persistence | Four parents and seven children passed; formatting, scoped vet and canonical generation passed | Repository and migration behavior only; no actual ownership handoff |
| Unit 2B private ownership | Four parents and nine children passed; formatting, ingress vet, canonical generation and whitespace checks passed | Process-local transition only; no gateway unlock, predecessor release or public handoff |
| Unit 2C authenticated handoff | Final-source run: ten parents and twelve children passed; formatting, ingress vet, canonical generation and whitespace checks passed | One authenticated route handoff; resource continuity and future once-release controls exercise the private ledger directly |
| Unit 3A completion persistence | Eight parents and eighteen children passed; fresh package builds, exact discovery, formatting, scoped vet, migration mirrors, canonical generation and whitespace checks passed | Purpose-specific SQL writers and one-transaction composition only; no authentic completion handle or executor caller |
| Unit 3B completion coordinator | Eight parents and seventeen children passed; compilation, exact discovery, formatting, scoped vet and canonical checks passed | Immediate same-process completion and replay only; no executor caller, later-worker recovery, Docker or race acceptance |
| Completion commit readback | Four parents and eight children passed; compilation, exact discovery, formatting, scoped vet and canonical checks passed | Same live handle and caller context; no process-loss recovery |
| Unit 3C executor integration | Eight parents and ten children passed; compilation, exact discovery, formatting, executor vet and canonical checks passed | Authentic empty/full predecessor completion and distinct second deployment; no later-worker recovery, broad suite, Docker or race acceptance |

These results are bound to their recorded source. Unit 2A's initial mirror test used the
wrong working directory; the corrected mirror checks passed. Unit 2B's initial
present-predecessor fixture dropped its retained charge too early. Adding
`retainAll` before fixture release reached the intended physical-state refusal;
that parent and its three children passed on the corrected binary. Three other
parent results were reused with identical production and an explicitly reviewed
test-only setup change. The original failures remain recorded.

Unit 2B directly checks that successor lease pointers and active state survive
ownership transfer and candidate/alias leave, that future exact retirement remains
reachable, and that invalid batches do not mutate ownership. Its competing-group
test requests both memory and disk against an exhausted snapshot. This establishes
combined refusal; it does not independently isolate each resource dimension.
Unit 2C separately checks memory and disk exhaustion for full and empty predecessor
rosters after exact predecessor release. These continuity and later once-release
controls call the private ledger directly. The separate Unit 3C evidence below
establishes a second full authenticated route activation.

The Unit 2C final-source run compiled the current six files, discovered exactly ten
expected parents, and passed all 22 parent/subcase names on 25 October 2026. Source
hashes before and after both the run and static checks match the reviewed freeze.
The run covers complete and empty predecessor handoff, refusal before unlock,
first and final unlock failures, final proof drift, authorization and receipt
commit acknowledgement loss, and cancellation after transfer followed by retry.

Earlier discovery, fixture-cleanup and fixture-location failures remain recorded.
An earlier formatted source-hash record was inadequate to bind the tested source;
the final structured record and fresh exact-ten run replace that acceptance claim.
The final run took seven seconds with a ten-minute harness limit. No production
deadline was changed. Source/security and independent final review passed for
this bounded local checkpoint; they do not authorize executor pause removal.

The Unit 3A final-source run on 25 October 2026 compiled the runtime-state,
deployment and jobs packages, discovered exactly eight parents and passed all
26 parent/subcase names. The run and static checks retained identical before/after
hashes for all eight new files. Root audit checked the three binary hashes, all
21 command logs and actual run/pass names; the 39-item composite artifact
inventory was also verified. The no-index checks of new files returned 1 with
empty output, indicating content differences without whitespace errors.

Review rejected the first candidate despite its passing tests. The corrected
source adds complete receipt equality, a running/noncancelling main mutation
predicate, and a fresh current-state guard before job completion. Focused tests
now cover each refusal. Earlier compile and guard-invalid fixture failures are
preserved; the final roster-drift fixture uses a permitted active-to-failed
transition and reaches the jobs guard. The final security and integration reviews
passed for the local persistence checkpoint. Its results do not substitute for
the separate Unit 3B coordinator checks below.

The Unit 3B evidence combines the initial eight-parent run with a fresh run of
the corrected lifetime parent and its three children. Only that test function
changed; production, helpers and the other seven parent bodies remained identical.
The revised test observes `Leave` entering its closing state before checking that
it remains blocked, and defers release of borrowed resources on failure.

Root audit verified 105 saved artifacts, current source copies, three binary and
command bindings, exact discovery and the actual 25 accepted run/pass names.
The whitespace command exited zero with one Git LF-to-CRLF warning. Earlier
interface/borrow failures, the crossed-job fixture's immutable-trigger refusal,
and its transaction-lifetime timeout remain historical evidence. The corrected
crossed-job test restores the canonical trigger before checking the guard.

Direct controls cover empty/full predecessor rosters, cancellation, changed
attempts, atomic rollback, commit acknowledgment loss, concurrent calls, retained
accounting proof on first completion and replay, and closed lifetime/proof loss.
Unresolved lifecycle history and mixed individual receipt states rely on the
unchanged lower-layer checks and source review here; no new Unit 3B-specific
positive recovery or mixed-receipt result is claimed. Review corrected the original
plan's unreachable later-attempt positive case and retained it as an E6 requirement.

Before executor wiring, completion now resolves an uncertain transaction commit
with one read-only transaction under the original live handle and caller context.
It verifies the exact receipt, current authority, active head, component rosters,
retained accounting and exactly one canonical completion event. A failed proof
returns the original commit error. It never repeats retirement, handoff or a
completion write to resolve that uncertainty.

The focused readback checks passed four parents and eight subcases on Windows.
They cover persisted commit acknowledgment loss, confirmed rollback, cancellation,
accounting drift, missing/duplicate events and the existing lifetime boundaries.
The first run exposed a test helper that incorrectly inferred an absent receipt
from an absent event. Its corrected event-integrity parent passed separately;
the other three parent bodies and production source were unchanged. Root verified
both retained binaries, exact discovery and all 13 raw/binary artifacts. Formatting,
scoped vet and canonical generation checks passed. This evidence does not exercise
the executor or recovery after process loss.

The lost-ack test counts the completion fault/write and terminal facts. It does
not separately count retirement or accounting effects. Their non-reentry is
established by review of the closed, read-only production branch.

### Executor integration

The executor calls `Complete` once while draining, using its live opaque retirement
handle. Only success returns `deployment_completed`. A failed completion keeps
the existing `previous_retirement` pause. The old generic successful-runtime
catch-up was removed: a succeeded runtime without its live handle cannot finalize
from persisted scalar state. Retirement handle cleanup still precedes candidate
cleanup. There is no automatic second completion, retirement or handoff call.

The final local evidence contains eight parents and ten subcases. The original
eight-parent run passed; review then strengthened two test oracles. Only those two
parents and their two subcases were rebuilt and rerun. The other six parent bodies,
eight subcases, all helpers and all production bytes remained unchanged. Root
audit verified the current five source files, both retained binaries, 25 bound
artifacts, exact discovery and the actual accepted run/pass names. Whitespace
checks passed with Git line-ending warnings.

The full-predecessor path uses two distinct authentic deployments and jobs. After
the second succeeds, an independent same-demand reservation is admitted at a
256 MiB capacity limit and released, proving that the predecessor charge was
released. At a 128 MiB limit it is refused, proving that successor capacity stays
charged. The empty-predecessor path completes without physical stop/remove.

A real completion-receipt precommit fault leaves runtime `draining`, main deployment
`waiting_health`, and the exact current job/attempt `running`. Activation,
retirement and handoff receipts remain; completion and its event are absent.
No legacy or physical cleanup runs on this path. Additional controls cover
post-activation proof loss, stop refusal, cancellation and finite pause attribution.
The handleless succeeded-runtime test starts with existing durable completion
facts; its refusal establishes missing live authority, not an absent SQL receipt.

The removed tests depended on generic successful-runtime finalization and
executor-owned cleanup. Their replacement controls cover authenticated completion,
closed proof loss, receipt-owned cleanup, single drain ownership and cancellation.
This change does not claim that a new worker can reconstruct those capabilities.

| Evidence | SHA-256 |
| --- | --- |
| Unit 2A corrected evidence | `1cfd6a39392f2f4be278e32086dffcb215be1b5c30aeb36555f1819188b5bd49` |
| Unit 2A root audit | `f5bf3869fdeac41cecbf28bdeadc56d0efb93553f8ccded356c48ac1d6caf40d` |
| Unit 2B corrected evidence | `61de0f8adc3a681f4cd7777bec4d446e200021c13d8c3f107b4f4465c5b1f656` |
| Unit 2B source/security review | `d5a1f26e7c9f0f8b47bbd083a71b44fca48798080907dd9cbf011c34fe4dc49b` |
| Unit 2B root audit | `15b031a2219c1ca4d8bf9b16496f02354703411923d05b2584a708dc0f00a831` |
| Unit 2C final-source artifact index | `6eb6f103654f88147f8ad90a63dac7687d8547f3c2061bf645e7525835907eb0` |
| Unit 2C final-source raw run | `29a7286313f5273511adf7116d248d47f2cee51fb4223e14502017806622479b` |
| Unit 2C static artifact index | `3738c1594c954d2ba2f0fc122fdab87db42c1e4d03b0c6a0618a7965caabd5b2` |
| Unit 2C source/security review | `b9fcb32905e0ef06318d177e158e846e67bced85c42a35b8ec838cc4e7f5f834` |
| Unit 2C final review closure | `391878d70531a2476aad3151e73cbcd593b95737ab1c754cfc0d4774bf10e908` |
| Unit 2C root audit | `fa1c452e9f9317ad0b6b59fc7e975b66325a462425d56ab9b2372b6ab1c094e4` |
| Unit 3A final composite | `273f0ab1e6adec6f465476ec89ee767f987ea88a6568e6d65f1e8bb034b9d4fb` |
| Unit 3A exact-eight run | `1ec72aa833d582ec9d94562b525525ba1e432fb96bdac449eb39e103a311d516` |
| Unit 3A static receipt | `0e7fcbb8105a8fb0e2afb7c212d8be6a98909fa856162dc792a5ca2b1878c46b` |
| Unit 3A source/security successor | `50e761e423617cdfb584515e7aa29735cc0fb56cb480721ec0336b374ade9e4a` |
| Unit 3A final review | `a52bbca1aa3faecd3dde45c9183a565d8b335c0c9c9a14a2e03c53097e08a5cd` |
| Unit 3A root audit | `282c7d1e7df9f84680e786ec77e7457b8a7d2d0626fdacdf0ec0d66713257123` |
| Unit 3B composite evidence | `b5b61c1a2330aae076e42a031f0188e25d74ba103c4634d94ea2e51da565ad45` |
| Unit 3B final source freeze | `53f992e97696b84fc2f58115fdfbf7fdc1e9bca47e3ba0d7f2ffb5a7438c601c` |
| Unit 3B source/security scope correction | `22d6b35a9a948c93deb2e7b0de81c340699ab9efa50b5038d09e467b018f0dcf` |
| Unit 3B root audit | `2c42ead324118f9889cb2d1798357c910e22b522f815e71ebeca00dd778ac5b6` |
| Unit 3B final integration review | `783c8fd65fc8323757497ce443717637a5fb44fb8af394fbf158c6e4ae3e0777` |
| Unit 3B source checkpoint | `b4b9caef73f266df97d58ee719118c482f8da195f7830ae17dc57a0d1c51f21a` |
| Completion commit-readback root audit | `dc347aa22c02414667887832ebe461123330d024ef4d97d266e91547670c3915` |
| Completion commit-readback source/security review | `4dcc55f0b322bf458f717cba0caf44ca4b610b51bced776a0f8afe8e7d65d7b1` |
| Completion readback review evidence clarification | `a79e4fa7c0a6bec629e3718d1fa55fc88151c719f6e92630e7341655fbb9521d` |
| Completion readback final integration review | `a2fd77f6c574f08a81d51ab0f6fb8ee7f2e12e6e9ae11a8636d3a082e89b0e2a` |
| Completion readback source checkpoint (`3bfb1ea`) | `f6a80bcaff120e0b9aa4b5426fd7830cb9614eedbee72f9e67d29b35f560c15a` |
| Unit 3C final composite | `9c67dc7f77a50a568ba5fb47f8d111b1fafeb005c0c6197f1b9623730849c0b8` |
| Unit 3C root audit | `2d7c92634290ad1cd8b2a6372294ddcabc73ff8f58c67fa5d887c9122e9299cd` |
| Unit 3C source/security review | `d5422cbe8c40e933edec9e75bebdbeef267a116363a00d1f241cd33a187fefe1` |
| Unit 3C final integration review | `d806d170c8ee8c2536b3405770cfcebc80247412fd6d9639085e22eb3f51371a` |
| Unit 3C review wording correction | `857d629c4ec98a78df51bd82aa062e8e823f6e21b748d6f15e067de9e383f930` |

## Scope and remaining acceptance

External databases remain application-owned and use scoped runtime secrets. This
work adds no managed database provisioning or application data migration.

Cross-process E6 recovery, hosted Docker and race checks, broader executor
regression checks and full M4 acceptance remain open. No live controller refresh
or deployment is part of these local checkpoints. A failed proof retains the
closed pause and required capacity charges. Rollback must restore the previous
executor pause with its matching contract and tests; stop new deployment work
before changing a controller version. Preserve immutable receipts, migration
history and protected history. Do not delete them or release successor leases to
bypass a failed proof. A database downgrade or production rollback is unverified.
