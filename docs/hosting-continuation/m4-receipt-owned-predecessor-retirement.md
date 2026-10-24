# M4 receipt-owned predecessor retirement

Status: Unit 1 source is committed at
`ac2aec823708d2c369e2457f7cc48a8bd0cda048` on
`feature/hosting-m4-receipt-retirement`. It implements immediate,
same-process retirement and immutable absence receipts. It intentionally leaves
the deployment in `Draining` with `route_reconciliation_required` and retains
all capacity charges. Accounting handoff, atomic completion and reconstruction
after process loss remain separate work. M4 is incomplete.

## Why this change is needed

Hosted Docker jobs on PR #222 reached route activation and then paused with
`previous_retirement/retirement_unavailable`. The existing activation receipt
proves the route commit; it does not grant a restarted or unrelated caller
permission to remove predecessor containers. Retirement needs authority tied to
that activation, fresh physical checks and the original live execution scope.

`ActivateWithPredecessorRetirement(ctx)` returns the activated deployment and an
opaque handle created internally from the committed activation result. Callers
cannot construct it from IDs or a receipt. The existing `Activate(ctx)` signature
remains available. The first entrypoint is sticky: a legacy activation cannot
later be upgraded into retirement authority. Repeated successful retirement
entry calls share one lifetime; closing any alias closes all aliases.

## Effect and history boundaries

The handle retains the exact successor and predecessor policies and complete
component rosters. Before stopping a predecessor, before removing it, and before
recording its absence, it rechecks the serving gateway, the healthy successor
roster, physical container identity and current transaction authority. The
authority includes the completed authorizing lifecycle operation, the current
worker Claim and the absence of unresolved application history.

The ledger reserves logical ownership of the whole predecessor roster before
gateway locking. It does not hold its mutex during SQL or Docker operations.
Stop and removal use the existing bounded command preparation and exact container
ID. Uncertain process termination latches the existing refusal state.

Migration `043_generated_route_predecessor_retirement.sql` is mirrored in the
public and embedded migration directories. It adds immutable component absence
records and a complete-roster retirement receipt. Database guards reject crossed
activation/component identities, incomplete aggregates, updates and deletes.
The aggregate requires a fresh observation that every predecessor is absent;
previous component receipts alone do not satisfy that check. The zero-predecessor
case uses an exact zero-roster receipt and retains active-successor accounting.

Retirement owns the drain delay. The executor closes the retirement handle before
closing its parent candidate execution. It does not fall back to legacy cleanup
when retirement authority is missing or refused.

## Failure behavior and remaining work

Identity drift, unhealthy successors, changed authority, reappearing predecessors,
stop/remove uncertainty and cancellation keep the job nonterminal. Successful
physical retirement also pauses in this unit because the remaining completion
steps are not implemented yet. Neither outcome releases predecessor or successor
capacity charges.

After a process restart, the private handle is unavailable. Durable receipts are
historical evidence and do not recreate effect authority. That path continues to
refuse retirement without effects or new retirement rows; recovery is the E6
follow-up.

The next units must add a once-only accounting handoff after successful gateway
unlock and an atomic transaction covering runtime state, the main deployment,
completion receipt, exact current worker attempt and job event. These are required
before a deployment can report successful completion.

This change does not add database provisioning. External databases remain owned
by the application and configured through scoped runtime secrets.

## Verification record

The Windows non-race evidence contains 12 unique parent tests and 16 child cases,
for 28 unique passing results. This is a composite of focused runs, with source
changes between runs explicitly reviewed; it is not one all-green aggregate run.

| Area | Accepted evidence |
| --- | --- |
| Migration | Three parents: fresh schema/mirror, crossed and incomplete records, immutable rows and exact zero-roster receipt |
| Capacity ledger | One parent and four cases: whole roster, partial/crossed refusal, duplicate command refusal and retained charges |
| Route retirement | Three parents and nine cases: exact physical retirement, legacy entry refusal, alias closure, identity/health/authority drift, physical stop refusal and reappearance before aggregate recording |
| Executor | Four parents: immediate receipt/pause, proof-loss refusal, injected retirement refusal with retained capacity, single drain ownership and cancellation |
| Activation lifetime | One parent and three cases: mixed entrypoints, concurrent entry and deterministic replay-versus-`Leave` serialization |

The executor's injected refusal checks orchestration; the route test separately
checks physical stop-command refusal. The immediate and proof-loss controls reuse
byte-identical test bodies. Reviewed source diffs bind those results to the later
drain and lifetime corrections. Reconstructed historical source is explicitly
labeled and hash-matched to its retained execution record.

Final static checks passed: `gofmt -l` on 13 changed Go files, `go vet` on
`./internal/database`, `./internal/deploymentexecution`,
`./internal/generatedruntimestate`, `./internal/generatedingress` and
`./internal/generatedexecutor`, bundled PowerShell
`scripts/check-generation.ps1`, and `git diff --check`. The focused builds and
exact test discovery are retained with their commands, binaries, output and
source hashes. All 15 source files remained unchanged during the final checks.

| Evidence | SHA-256 |
| --- | --- |
| Fifteen-file source manifest | `d0cfd4db7419e9ff456d1e9dd4ddef4448fbc15c5e6cc7b384b6fbf59a208354` |
| Composite receipt | `130125f5ef7d5cdf9911deb1042a443f741b36a655c8e17db761ed1709f2f729` |
| Source-impact record | `6d04e874a032ccb0bbfc990d8ec495aa87f7276d03ed77c5d4e3108757c5122e` |
| Independent audit of 86 artifacts and 28 results | `238bda24615da0adfddbe3c82b8d203d9ea55532c13c9b6daa63204efc755c39` |
| Final alias-lifetime security review | `5b79bdb662adc0e253a6984d88a746d6bfe4acc6132482d183ec26808ddc2d6d` |
| Final integration review before CI-parent integration | `0e6c5b747860f80819457e66866f9a08da01f3d40fdf3e73de86a067e2885fb5` |

### Corrections and preserved failures

Review found and corrected a redundant executor drain wait and a race between
activation replay and closing the shared retirement handle. The latter now uses
the existing active-borrow protocol; the deterministic regression establishes
serialization and closure without claiming race-detector coverage.

Earlier failures remain recorded: an incorrect migration-test working directory,
a capacity test that used a permissive 512 MiB fixture instead of its required
128 MiB bound, an unused-callback concurrency harness timeout, a PowerShell
argument-parsing error before discovery, and earlier handoff diagnostics. The
corrected affected controls passed. Failed aggregate commands are not relabeled
as passing; only their individually completed tests are included where the
source-impact record justifies reuse.

### CI-parent integration

The source commit preserves PR #222's `69e0ef4` baseline. A conflict-free local
merge of published PR #223 at `bbf6a0f18261b5053759cdcb75d9117d341b6b4e`
produced `c7ab204d5603fd15ab199236881c34bbf1853929`. This inherits the CI
partition repair while leaving all 15 retirement source files byte-identical.
The diff against PR #223 contains only the retirement implementation and this
document. The integration identity receipt is
`7001f377e92bb95a527dbc901d52f11280a641fea4c16f3447f9ca3e652e9520`.
Integrated compile and generation checks passed. Exact discovery found the 12
Unit 1 parents in retained test binaries. A newly compiled integrated ingress
binary discovered 1,237 parents; running the actual selector branches assigned
every parent exactly once across 33 nonempty CI suites, with no omissions or
overlaps. The shell contract passed using the pinned real `jq`. These checks
establish compilation and selection, not additional behavior or race results.

The integrated receipt is
`9b1141d68912ce6a2763c57374d19d85870508e42819d928d1476c60f1da7f9b`;
the independent audit is
`38db3aca5a482d1f0759662312cda555089fa5c2ad2cd1be14218ea52685aca1`.
Two earlier verification-harness failures are retained: an omitted `jq` argument
terminator and missing workflow inputs in the shell-contract sandbox. Corrections
changed only the verification harness. All 15 production/test source hashes
remained unchanged, and no behavioral suite was replayed for this integration.

No manual live-controller, native Linux, local race-detector or Docker acceptance
is established. Hosted verification is required after draft publication. Units
2–3, E6, full CI and the rest of M4 remain open.

## Rollback and operational limits

This is an unmerged development branch. No deployment or live-controller refresh
is part of its delivery. The additive migration preserves existing history and
has no destructive down migration. Do not delete retirement records or manipulate
active runtime rows to undo a partially completed retirement. An affected
deployment must remain paused with its capacity charges retained until an
authorized recovery path can re-establish all required proofs.
