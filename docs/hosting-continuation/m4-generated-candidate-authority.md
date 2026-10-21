# M4 generated candidate authority — draft integration

This branch continues the generated network, image and migration authority work. Its purpose is to bind candidate creation, start, health observation and explicit inactive cleanup to the original deployment authority and immutable creation receipts.

**The candidate owner is wired into the engine, executor and controller, and the migrated default tests pass in the bounded checkpoints below. This document does not establish E4 or M4 acceptance.** The foundation commits remain part of one cohesive candidate branch. Local checks do not establish Docker or race acceptance.

## Boundaries

- One guarded concrete repository owns candidate commands, protected plan/configuration reads and result evidence. Public container identities do not grant command or capacity-release authority.
- Runtime secrets remain scoped to the selected application configuration. External databases remain application-owned. There is no managed database or Neon provisioning.
- Candidate and retained lifecycle consumers share one raw host capacity budget. Copies of the raw budget share its private accounting state.
- An immutable `generated_candidate_intents` receipt preserves creator job/attempt, selected configuration, selected image artifact/attempt, environment staging identity and fixed policy identity. It is inserted with the component's transition to Starting in one caller transaction.
- A receipt establishes intent, not physical command entry. The original known-never-entered reservation may release unused capacity. Later receipt aliases conservatively retain that exact charge.
- Explicit cleanup may retire an exact charge after canonical absence and fresh cleanup authority. Receipt alias retention does not defeat that cleanup. Explicit whole-reservation retention and unquiesced mutation remain irreversible and prevent capacity release.
- Confirmed-absent receipts cannot reacquire capacity. Retired aliases cannot act on a fresh charge epoch or prevent release of unrelated unused siblings.
- Cleanup tokens join a shared cleanup count. FinishHealth must close cleanup and join it before acquiring the complete roster. Closing remains irreversible across failed commits and repins.
- Terminal failure requires a private observed create/start/health failure, canonical absence for every original receipt-backed invocation, completed environment cleanup, and one guarded whole-roster transaction. Manual cleanup, a staging refusal, timeout, cancellation or a failed read cannot substitute for that evidence.
- Generic main/runtime/component failure writers atomically refuse a receipt-owned deployment. A caller that read state before BeginCreate cannot use that stale read to overwrite the owner's later intent.
- Candidate-only repository composition validates the exact database, guard and protected stores, and preserves the same owner registry across repository copies. It does not enable migration commands.
- A protected runtime export containing only its header does not create an environment file. A configured key with an explicitly empty value still requires secure staging. Exact revision checks and clearing of decrypted bytes apply to both paths.

## Head generation provenance

The existing runtime Begin transaction persists predecessor deployment ID and slot, but no generation. Capacity identity therefore must not claim a persisted predecessor generation.

The concrete owner separately captures an optional process-local head snapshot after attaching risky capacity. An exact predecessor snapshot is bound once; a later pin cannot refresh it after drift. An exact self head can support only read-only route observation after the owner verifies SwitchingRoute and the complete Active roster. It cannot reconstruct an unknown predecessor generation or grant start, cleanup, transfer or completion rights.

An unrelated head refuses execution while already-attached risky capacity remains charged. If raw capacity acquisition itself fails, no reconstructed charge is claimed. Cold-process accounting recovery remains part of E6.

## Recorded foundation evidence

All accepted local Go checks below used pinned Go 1.26.7 with `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`. Linux checks used a cross-compiled ELF test binary executed natively under WSL. These are non-race checks.

| Unit | Actual result on each platform | Evidence limitation |
|---|---|---|
| Receipt schema and shared raw budget, foundation attempt 02 | 75 parents + 113 children = 188 passing cases; no skips or failures | Includes full existing generatedruntime package and selected database migration tests; no candidate command-owner integration |
| Candidate scope/cleanup guard, attempt 02 | 14 parents passing; no skips or failures; vet passed | Covers scope lifetime and existing migration cleanup regressions, not physical commands |
| Private candidate ledger, attempt 4 | 14 parents + 6 children = 20 passing cases; no skips or failures; vet/format passed | Bounded independent security review passed. The generation-field amendment is included in the subsequent factory/head checkpoint |
| Five candidate transaction helpers, attempt 02 | 8 parents + 29 children = 37 passing cases; no skips or failures; vet passed | Six new parents plus two existing migration transaction regressions. Exact protected plan names, current Claim, head, policy and physical evidence remain owner responsibilities |
| Container projection/hardening, attempt 03 | 8 parents + 148 children = 156 passing cases; no skips or failures; vet passed | Uses the actual generated-image entrypoint `/usr/local/bin/rig-entrypoint`; real Docker behavior and owner integration remain pending |
| Singleton factory, head anchors and ledger amendment, attempt 8 | 27 deployment parents + 25 children = 52 cases, plus 8 raw-capacity parents + 5 children = 13 cases; no skips or failures; four-package vet passed | Includes direct and ancestor symlink/junction refusal, copied-repository singleton identity and seven head-registry parents. Independent bounded source/evidence review passed. Owner execution and race behavior remain unverified |
| Protected policy and validator, attempt 3 | 17 parents + 35 children = 52 passing cases; no skips or failures; vet passed | Earlier fixture and WSL harness failures are preserved. Does not establish real Docker acceptance |
| Concrete owner, attempt 4 | 27 parents + 42 children = 69 passing cases; no skips or failures; four-package vet passed | Includes rejected foreign cleanup identity, shared unquiesced health refusal, partial environment staging cleanup, and no SQLite writer during Docker execution. This checkpoint predates the observed-failure proof addition |
| Observed-failure path, attempt 02 | 37 parents + 55 children = 92 passing cases; no skips or failures; two-package vet passed | Includes owner/policy/validator regressions and atomic late-write rollback. Earlier two fixture-assumption failures are preserved. The private proof is produced by actual owner methods in tests |
| Candidate-only composition and generic failure barrier, 20261020T232332Z | 5 parents + 3 children = 8 passing cases; no skips or failures; two-package vet passed | Three observed-failure regressions plus foreign-store/copy and stale-caller failure checks. No broader new package acceptance claim |
| Executor sealed-authority migration, attempt 17 | Exact 53 parents + 54 children = 107 passing cases; no skips or failures; vet on both targets, formatting and diff checks passed | Tests use real protected stores, claims, candidate owner and stateful Docker observations. A later coverage audit found four lost assertions despite the matching parent census. The restored assertions and empty-configuration production correction are not covered by this earlier checkpoint; aggregate verification remains pending |

The state-helper attempt 01 used system Go 1.27.0 and is retained only as superseded evidence. Parser attempts 01–02 are also retained as superseded evidence: review added network/receipt boundary coverage, then source inspection corrected the generated image entrypoint. None substitutes for the accepted attempts above.

Evidence receipts are retained under the task's `temp/m3-lan-external-db-20261015/m4-generated-candidate-authority-verification` directory; the ledger worker's attempt 4 and factory attempt 8 are under `temp/m3-lan-external-db-20261015/CandidateEvidence`. Superseded factory attempts remain preserved, including fixture/harness failures. Attempt 8 is the accepted factory evidence.

Observed-failure evidence is under the task workspace's `CandidateEvidence/candidate-observed-failure-attempt-02`. Its receipt records the exact selected tests, toolchain, source hashes and commands. Composition evidence is under `temp/m3-lan-external-db-20261015/m4-generated-candidate-authority-verification/composition-20261020T232332Z`. The Windows controller build passed in `production-build-20261020T231214Z`; later fixture edits are not covered by that earlier build.

Reviewed foundation commits are `e64ec5e` (fixed state transactions), `8a36073` (physical projection and hardening), and `bdbd888` (factory/head registry and capacity amendment). These commits do not by themselves establish acceptance of the candidate family. The subsequent owner, caller and fixture integration is covered by the checkpoints below.

Executor evidence is under `temp/m3-lan-external-db-20261015/CandidateEvidence/executor-sealed-tests-attempt-17`. Its checkpoint summary and source manifest record 32 local dependency packages and 294 Go files. Earlier attempts are preserved. Independent bounded security review confirmed the production candidate owner and independently counted the earlier owner, failure and composition results. Its follow-up verified that removing the unused Engine fields was the only later production delta. These selections overlap; their counts must not be added together as distinct coverage.

The local CodeRabbit CLI was available at version 0.9.0, but authentication reported `not_authenticated`. That review did not run and supplies no source approval. Hosted review remains a delivery gate.

The independent executor coverage audit identified lost checks for competing capacity admission, completed approved-migration replay, preservation of an existing active deployment on replacement failure, and nonempty policy-findings forwarding/copying. Those assertions have been restored and executed using the real protected owner. A separately valid competitor shares the same budget; a same-receipt alias is not a competing admission test. The completed-migration replay adds one child, and the gateway-readiness parent is renamed from `PersistsGatewayReadinessFailureAndPreservesOldHead` to `PausesGatewayReadinessFailureAndPreservesOldHead` to describe its actual pause contract.

Runtime migration diagnostics also isolated a Windows fixture failure inside protected plan persistence. An independent short test root makes the same lifecycle parent pass; production path validation was not relaxed. Subsequent checks exposed the header-only empty-export staging regression, which is corrected in the concrete owner. Independent bounded security review accepted that source delta.

The final runtime checkpoint passed on Windows and native Linux: runtime 61 parents / 141 cases, lifecycle 6 / 9, image 10 / 31, and ingress 1 / 1; zero failures or skips. Windows four-package vet passed. Evidence is in `runtime-test-migration-20261020T191701Z-post-empty-fix`; its directory mistakenly uses Chicago local time despite the `Z` suffix, and its receipt records actual UTC completion at 2026-10-21T00:23:17Z. Rejected PowerShell argument forwarding and a nine-parent image selection remain preserved; the accepted Linux image rerun executed all ten intended parents. The new explicit public `PUBLIC=` test proves that an empty value still stages the protected export and uses `--env-file`.

The first aggregate checkpoint (`integrated-callers-20261021T002644Z`) passed 52 executor parents and failed one restored assertion: after the first component started and authorization was lost during the sibling network inspection, the executor released that known-unused sibling's charge. A distinct valid deployment could then reserve it. The owner-level release was intentional; the executor had lost its prior whole-aggregate retention on uncertain cleanup. The executor now requests whole-reservation retention before returning that uncertainty. It preserves the owner's private cleanup and terminal-failure proofs. Focused security review accepted this correction, and all 53 executor parents / 108 cases passed on both platforms. Whole retention still refuses atomically if another member was already authoritatively retired; risky individual shares remain charged, and this repair does not claim a stronger partial-retirement policy.

## Integrated caller checkpoints

All selections below have exact discovery and zero failures/skips in their accepted executions. They do not add together with overlapping historical selections.

| Evidence directory | Windows and native Linux result | Scope |
|---|---|---|
| `integrated-callers-20261021T003020Z` | 53 executor parents / 108 cases on each | Complete executor suite, including restored capacity, completed-migration replay, old-head preservation and findings assertions. The subsequent lost-ack test in this attempt failed a stale expectation, so this receipt is not an all-checks pass |
| `integrated-callers-20261021T003320Z` | 2 lost-ack parents on each | Real SQLite commits followed by injected acknowledgment loss. Original no-permit/never-entered capacity may release without rewriting durable intent; a later receipt attachment reacquires and retains. Health handoff remains closed after lost acknowledgment. The later helper check exposed a fixture update bug |
| `integrated-callers-20261021T003455Z` | 7 helper parents and 15 controller-composition parents / 72 composition cases on each | Protected plans/configuration, selected revision versus release-original revision, immutable replay, controller composition and refusal paths |

The shared fixture now compares canonical protected exports (including the header and sorted entries). Its update request removes only omitted keys, avoiding a contradictory remove-and-replace for the same scope. The selected-configuration test verifies unchanged reordered values reuse the revision, changed values create a new revision, and omitted keys are removed. These changes affect test support, not application configuration validation.

Live-tag compilation passed for generated image, ingress and controller test binaries on both targets in `integrated-callers-20261021T003455Z`; the same checkpoint passed vet for executor, deployments, shared helper and controller on both targets. Compilation is not live execution.

`supplemental-20261021T003815Z` passed Windows/Linux `go build ./cmd/hostd` and `go vet ./...`, the canonical `scripts/check-generation.ps1`, documentation workflow/build/accessibility checks, aggregate diff checks and formatting checks. Two inherited fixture files use CRLF; byte comparison after LF normalization confirmed their gofmt output is otherwise identical. No source newline normalization was performed. The first supplemental attempt records this harness-only formatting refusal. All tracked and nonignored source hashes were stable throughout the accepted run; this results paragraph was added afterward. Hosted Docker and race results remain required.

Published migration diagnostic prerequisite `f5a0006` was integrated with history preserved at `0276c34`. Its four changed files contain test support and evidence documentation only. The combined-tree checkpoint `resume-diagnostics-20261021T004311Z` passed both new diagnostic parents on Windows/native Linux, live controller compilation and vet on both targets, canonical generation, documentation checks, and diff/format checks with source hashes unchanged. The earlier `004259Z` formatting attempt refused checkout CRLF; the accepted check proves normalized bytes equal gofmt output without changing source. No unchanged candidate production selection was counted as newly rerun. Final integration review separately checked this small prerequisite delta.

## Docker fixture migration

Direct fixtures now prepare protected plans and scoped configuration before building their images. They reserve capacity before selecting a Ready artifact and use the real candidate owner to create, start, record health and hand off. Route state is recorded only after comparing an actual ingress observation to the complete owner-derived endpoint roster. The fixtures no longer invent successful container IDs or mark an unstarted sibling Active.

The external PostgreSQL/HTTPS fixture accepts an API-only plan matching the container it actually builds and runs. It checks build/runtime phase separation on that component. It does not claim frontend execution; complete frontend/backend execution belongs to the compiler/controller acceptance gates. The gateway readiness fixture keeps a handed-off, gateway-rejected candidate unresolved, and exercises successful replacement on a separate application rather than fabricating recovery history.

Disposable Docker teardown validates the exact created ID and ownership labels. It is test cleanup, not evidence of controller predecessor retirement or capacity transfer. These migrated fixtures have not yet received live Docker acceptance.

## Remaining delivery gates

1. Independent final integration review accepted the bounded local candidate slice for draft publication. Hosted and complete milestone acceptance remain pending.
2. Run hosted Docker, race and CodeRabbit gates against the published head, preserving failed evidence and fixing demonstrated regressions.
3. Finish E5 route/retirement/completion and E6 recovery before claiming complete controller acceptance or M4 completion.

E5 owns route publication/restoration, active-head CAS, predecessor retirement, accounting transfer and completion. E4 must refuse legacy removal of receipt-owned predecessors and leave that replacement in Draining with route reconciliation required. Two-replacement success is not an E4 acceptance claim. The existing complete controller journey still requires replacement success and remains a delivery gate; its requirement is not weakened to accept this intermediate pause. E6 owns legacy/cold recovery and completion repair. No GitHub target merge or live activation is authorized by this work.
