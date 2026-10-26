# Recover accounting handoff after predecessor retirement

Status: implemented from [PR #257](https://github.com/tyhuang9/rig/pull/257), with focused local recovery and failure checks passing. Hosted Docker and Linux race acceptance remain pending. This unit does not complete M4.

## Required behavior

A deployment can finish removing its predecessor and record the complete retirement receipts, then lose its controller process before recording the capacity handoff. Its successor may already be serving. Restart must account for the retained resources and let a fresh authorized worker finish that exact deployment without repeating container operations.

This change covers two durable states:

- Exact activation (042) and complete component and aggregate retirement (043), with no accounting authorization or handoff receipt (044).
- The same evidence plus the exact accounting authorization, but no handoff receipt.

The second state does not reveal whether the lost process transferred or released its old capacity token. A new runtime must conservatively reconstruct both exact debts before performing its own verified handoff. It must retain successor capacity throughout and release only its newly restored predecessor capacity once.

The existing [recovered completion](m4-recovered-route-completion.md) path handles a completed handoff with no terminal receipt (045). Earlier crashes before complete physical retirement still require separate recovery work.

## Authority and failure behavior

Durable receipts establish historical identity; they do not grant permission to execute a new effect. Recovery must bind the current worker attempt, desired-state revision, complete immutable receipt chain, protected gateway generation, exact component roster, predecessor absence and currently served successor route.

The successor must retain its exact configured identity and running state. A health-only degradation does not erase capacity ownership or prevent accounting for an already-serving container. Existing live-retirement health requirements remain unchanged.

Partial, duplicate, crossed, unclassified or drifted evidence must refuse. Old handles cannot acquire new permission. Accounting and completion preserve immutable creator provenance while validating the current worker separately.

Keep the existing ordering of authorization commit, ownership transfer, gateway unlock, promotion, predecessor release and handoff receipt. Hold exact roster lifetimes through verification and transaction readback, without holding coordinator locks across SQL or external observations. A failure before the verified release boundary retains both debts. Recovery must not build, migrate, create, start, stop or remove a container.

Repeat predecessor-absence and successor-identity checks under gateway exclusion at each write and readback boundary. After an in-memory transfer starts, an error must retain the exact operation for a safe retry or refuse further mutation until restart; it cannot discard partial accounting progress and create a new operation. An ambiguous receipt commit requires an exact durable readback. Unreadable or conflicting evidence must refuse further mutation.

External databases remain application-owned and receive only their configured scoped runtime secrets. This work does not change database contents or provisioning.

## Observed baseline — 2026-10-26

The regression `TestGeneratedDomainRestartAccountingRetiredPredecessorRecoversHandoffWithoutSecondShare` uses real activation and retirement to create 042 and complete 043, then closes the original handles before fresh cold construction. It verifies no relevant 044/045, an absent predecessor, an active running successor, insufficient spare capacity for another allocation, unchanged SQL/protected history and unchanged predecessor effect counts.

On base `73bce73fc3a5facbc6b9389c37afae7d0112e0d9`, fresh pinned Go 1.26.7 compilation and test discovery passed. The selected regression failed in 1.276 seconds at `cold_construction/cold_reservation`, after its producer and preservation checks passed. The same binary passed the existing lifetime/proof-loss parent and its three controls in 2.401 seconds.

| Evidence | SHA-256 |
| --- | --- |
| Frozen regression source | `c284a1b20f36c7f56f4680c3d7f1ee6b8467ecc17b091c7ad21d40e4de92abed` |
| Baseline execution receipt | `5c65c2e11ac30d4fc480bf51712ed7e81fd172c6a451548ce22a3521c4316f68` |
| Expected failure output | `82723c30d72a12294724000ffbc61d444dabd0b033dca49cc6a9c4c5d40caba8` |
| Lifetime/proof-loss control receipt | `a8f86069a62a48c0b6873a4b3e940605d8373793ba2085a3840077f667b8c0bd` |

These baseline results establish the original refusal and a preserved control. The following final run verifies the implemented recovery locally. No local Docker or race execution is claimed.

## Local acceptance — 2026-10-26

The final source-bound run used Go 1.26.7, `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false` and `CGO_ENABLED=0`. Fresh ingress and `live_docker` hostd binaries compiled, and all expected parent tests were discovered.

- **29 unique cases passed:** 10 parents and 19 children. The 37 raw passing records include nested process repetitions. There were no missing, failed or skipped expected cases in the accepted run.
- Both authentic public cold-recovery parents passed in 3.411 seconds total: `TestGeneratedDomainRestartAccountingRetiredPredecessorRecoversHandoffWithoutSecondShare` and `TestGeneratedDomainRestartAccountingAuthorizedHandoffRecoversWithoutSecondShare`. They close original handles, construct cold state before fresh recovery authority, preserve receipt lineage and effects, and verify exact predecessor release with successor debt retained. The successor remains configured and running despite a health-only degradation.
- The 2.871-second ingress group includes exact dual-roster transfer, nine malformed-prefix controls, ordinary handoff compatibility, and two new private recovered-entry fault controls. One commits the real handoff receipt then loses its acknowledgment; exact readback resolves one write and refuses a duplicate retry. The other changes predecessor physical evidence at the final proof boundary; it requires fail-stop, no handoff receipt, predecessor debt released once and successor debt retained. These private-entry controls are separate from public cold-recovery coverage.
- Existing completed-Current replacement and interrupted-Draining debt parents passed in 3.514 seconds. Pure hostd boundary controls passed in 0.030 seconds; they do not execute Docker.
- Normalized `gofmt`, `go vet ./internal/generatedingress`, `go vet -tags live_docker ./cmd/hostd`, `scripts/check-generation.ps1`, source-stability checks and `git diff --check` passed. Source/security review closed the scoped findings.

| Final evidence | SHA-256 |
| --- | --- |
| Nine-file Go source manifest | `932c618102f39caa1e31f100346ad45556afc0f6818248e72f94529e541f347b` |
| Compilation, discovery and execution receipt | `11ff94a698fbf8d7a83342ee9268c0f6abc9be63da75e7ba6de826002815110b` |
| Accepted test census | `9202eec1af000efa83ea1b4b51b434c0887ef55cb4e087bc258c64203fb6ad86` |
| Static verification receipt | `b4a30e69613c6e898667d2585521e83643c9ce560ed42baa8c7939f27ad14fe4` |
| Source/security execution closure | `5e665b94f538e92b124bf543d88db2a2028133f894786225ee0589e6cd9412f4` |

Earlier development failures are retained separately: absent-predecessor bootstrap rejection, scoped fixture setup and debt-oracle errors, and the extra recovery-only health requirement. They are excluded from the final accepted census. The final production correction preserves original live-retirement health requirements and all recovered identity, running-state, route and authority checks.

## Hosted fault/restart coverage

The new live case is `TestLiveControllerGeneratedProcessKillRecovery/after_predecessor_retirement_before_handoff`. It uses three controller processes against the same database, protected root and immutable deployment job. Fixed test-only SQL faults stop the first process before accounting authorization and the second before the handoff receipt. Each fault must be removed and its exact durable and physical state checked before the parent terminates and reaps that process. Fault cleanup has a five-second deadline. The third process must recover through handoff and completion with two fresh worker attempts. This actual Docker journey remains unexecuted locally.

The existing post-handoff completion case remains required. Both cases are fault/restart coverage; they do not claim a natural process kill inside a particular SQL statement. The existing 47-minute journey, 46-minute child, 49-minute Go package, 50-minute step and 80-minute job limits are unchanged. Actual hosted execution must establish that the added case fits those limits.

The exact workflow gate accepted one complete eight-boundary result and rejected all 36 missing, skipped, failed or duplicate parent/child variants. This check uses synthetic Go JSON and proves gate enforcement only. Receipt SHA-256: `6f9318ffe5909a3fb0586894065cb23b11c61d952910e85f0ae00b877f70e238`. Workflow SHA-256: `8aa4d73110dff1f43673a06dcf6463d7349358f78006005abed2fb1f032a4ee9`.

## Rollback

Preserve existing receipts, history and capacity evidence. Reverting the recovery implementation may restore refusal at this prefix; it must not fabricate completion, erase retained debt or repeat physical retirement. This unit introduces no new schema or durable record format.
