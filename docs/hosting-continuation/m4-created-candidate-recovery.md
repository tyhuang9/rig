# Recover a candidate created before controller restart

Status: the complete local repair is verified on top of [PR #262](https://github.com/tyhuang9/rig/pull/262), head `e1bfb028bac3a9bbf98b99b75ff68c0a93c5ac98`. It recovers the exact candidate prefix and capacity, completes genuine mixed-creator history041–045, and verifies the resulting startup snapshots through the production replay predicates. Fresh-process hosted Docker acceptance and M4 remain incomplete.

## Problem

Candidate creation records the immutable container ID before the controller starts that container. The durable component state is named `running` at this point; that value alone does not prove that Docker start occurred. The existing scoped worker accepts an exactly configured container in Docker's never-started state and can start it under fresh authorization.

On that baseline, cold reconstruction requires that this component already be physically running. A controller interrupted after creation but before start therefore cannot reach the existing worker recovery path. Both local regressions now establish that exact refusal before the production repair.

The executor creates and starts each sorted component before proceeding to the next. In the hosted two-component replacement, a stop before the first candidate start leaves `api` created with its immutable intent/ID, while `frontend` is still image-ready and absent, with no candidate intent. The previous deployment is still serving. An all-created or one-component fixture does not reproduce this prefix.

There are two further requirements. Cold reconstruction must restore the complete existing candidate reservation, including its absent tail, rather than acquire another raw capacity share. A fresh worker creating that tail also records its own creator attempt. The baseline activation reader and migration046 require a uniform creator attempt across the roster, so admission and accounting changes alone cannot complete this authentic journey. The repair preserves each immutable creation receipt while separately validating the current activation writer.

## Recovery contract

Cold construction may reconstruct accounting for an exact created-but-never-started candidate only in the durable starting-candidate phase, with the already-recorded immutable ID, candidate intent, application, deployment, release, component, image, configured policy, network and ownership evidence. All existing provenance and topology checks still apply.

Never-started means Docker reports `created`, a valid zero start timestamp, no running, paused, restarting or dead flag, and exit code zero. Previously started, stopped, exited, foreign, missing or contradictory containers cannot use this exception. An exact already-running earlier component continues through the existing running-proof path. Name-only creation uncertainty cannot become an immutable identity through reconstruction.

Construction is descriptive: it must not create or start containers, publish routes, write receipts, alter the deployment head or reserve another capacity share. It restores the exact existing charges. A fresh scoped worker revalidates current authority, adopts the same immutable container and invokes the existing guarded start path once. Repeated adoption of an already-running candidate continues through the existing running-observation path.

The absent, image-ready tail must remain absent during reconstruction. A fresh authorized worker may then create it through the ordinary guarded path with truthful creator provenance. Do not invent an old intent, receipt or creator attempt for that component. Full-roster ownership and accounting remain required even when only a sequential prefix has physical containers. Repeated interruption and retry must retain the same guarantees across more than one creator attempt.

An absent tail has no receipt to retain, but its capacity charge must remain explicitly retained. Releasing a failed fresh worker must not discard that charge or require another raw reservation. Complete current-route accounting and existing waiting-health recovery retain their separate contracts.

Initial deployment and replacement deployment both require verification. Initial recovery has a canonical empty head and the full candidate roster's charges. Replacement recovery must preserve the served predecessor, its immutable head and history, and both deployments' full roster charges while restoring the successor. An initial-only test cannot establish the hosted replacement boundary.

## Verification to record

Produce each state using real guarded reservation, pinning, creation and durable identity recording; stop before start and leave the old handles and effects scope. The replacement case must include the actual two-component prefix and complete served predecessor. Run ordinary runtime recovery, reconstruct the cold domain, then obtain a fresh worker claim. Do not fabricate seals, immutable receipts, a predecessor head or capacity ownership.

Before the repair, require refusal at `cold_construction / cold_group_container_attestation` after verifying the intended prefix. After the repair, verify nonmutating construction, exact head/history/protected-state preservation, unchanged physical-effect counts, restored accounting without another reservation, and fresh adoption of the same container followed by one guarded start. Complete the missing tail and activation with the actual per-component creators and fresh writer, preserving predecessor retirement and accounting history. Verify both initial and authentic replacement journeys; a passing early construction check alone is insufficient.

Failure controls must reject crossed phase, immutable identity, candidate intent, policy, image, network and authority, along with nonzero or invalid start time, missing containers, and exited, paused, restarting or dead states. Preserve conservative debt and the existing running-candidate and predecessor-retirement behavior. Record exact discovered cases, frozen source, commands, raw results and limitations; passing local simulated-process tests do not establish Docker acceptance.

## Recorded baseline evidence

The final baseline packet, `red-sequential-prefix-v5`, freshly compiled `internal/generatedingress` and discovered `TestGeneratedDomainColdRuntimeBoundary` before executing its exact initial-created and replacement-sequential-prefix children. Both exited1 at the intended `cold_construction/cold_group_container_attestation` assertion, after preservation checks. They completed in under one second each, with no skip, panic or timeout. This is qualified failing-baseline evidence, not a passing repair result.

The replacement fixture completes genuine generated predecessor history041–045 and serves its two-component roster before constructing the successor prefix. Its lifecycle binding is `legacy_uninitialized`; it does not establish the hosted managed Running-authorizer path. The unchanged hosted replacement gate remains required for that claim.

The precompile23-path manifest has SHA-256 `71fc72343bdcdc49471c15a80696bf525212be71421dfacf5c3501471b485b76`, with composite `ad8528c38f4e04178e42e7fae0afa52b999cc88eb41b58f913db129f591f6794`. Independent audit `9cdab3647ecb214fb7daf85ea3e0421f0dccbce575487d033ad67e309318e055` verifies source copies, the committed production baseline, binary, receipts, discovery and both exact child failures. Earlier fixture compile/setup failures are preserved and excluded.

## Verified local reconstruction checkpoint

Local commit `dfc2bf14680357a14a5406f42596628899d4368e` adds the exact sorted starting-candidate prefix proof and restores its full capacity debt. It changes two production files and two test files. The valid prefix may end in one never-started container; an absent tail keeps explicit retained capacity without a fabricated receipt. Invalid receipt-bearing starting-candidate shapes refuse rather than falling back to a new raw reservation.

The final `unit1-closure-v4` packet passed four discovered parent selections,22 distinct cases and40 raw pass records, with no failure or skip. Repeated parent records come from subprocess isolation and are not additional cases. The seven-case public construction parent passed in5.15 seconds at the original90-second deadline. Coverage includes all-running and sequential partial prefixes, invalid ordering, multiple never-started containers and a stopped final container, exact debt-token graph controls, plus selected zero-created, waiting-health, initial-created and genuine replacement controls. The resumed replacement reaches health; this checkpoint does not claim mixed-creator activation or completion.

Read-only `gofmt -d` for the four changed Go files and `go vet ./internal/generatedingress` passed. The24-path precompile manifest is `6bce5fa306aa3e96731146e67a7ad2c6d1030db1a24d7ca5ceab20c4dbd7c6db`, composite `5d77c0d1c4d83d953cc9f1eb5453894df9f9621a35a911caea0c6330a825d493`. Independent native audit `c9de0fa7d48fbc4d992fa57700850f3458556fccb3ebf1c278adae6d1cb6f02a` binds eleven accepted stages. Current-source/scope closure is `7de023f5ede5c731d5ac785e7b8ad79a758918334b8a012abd93e47b7ea6cfa4`. Independent source, security and integration review passed for this local checkpoint.

Earlier fixture compile failures, image-format refusals and grouped-test timeouts are preserved and excluded. The timeouts were traced to a new assertion re-locking its held accounting mutex; the final assertion checks the same exact fields under that lock, and the full canonical parent now passes. No deadline, production guard or required case was weakened. No full repository, Docker or Linux race result is inferred from this local checkpoint.

## Mixed-creator activation and history

The extended genuine replacement test reproduces the activation failure on the Unit1 commit: it verifies mixed creator receipts and fails at `stage=activate` with `generated route activation creator: generated runtime active head conflict`. The `unit2-mixed041-red-v2` packet is a causal failing baseline. Its registry wrapper ran the full parent despite an anchored child argument, so it is not claimed as an isolated child run. All other executed children passed. Independent source/receipt/output audit is `2357a932e39abf2037aa44aaf043e3a8412876a00e91562d6fe5e62781d55e65`.

The implementation uses the minimum genuine creator attempt across the exact same-job component roster as the activation creator, while checking the fresh activation writer separately. Every component creator must be no later than that writer. Additive migration047 replaces only the activation-insert trigger, preserving its existing authority, binding, active-head, topology and health guards. It preserves old rows and digests and legacy homogeneous replay. It rejects incomplete rosters, incorrect minima, future creators and missing or crossed writers. The genuine replacement now completes activation042, predecessor retirement043, accounting handoff044 and completion045 while preserving the original per-component041 receipts.

A second `NewGeneratedDomain` call in the same process joins the published owner; it does not prove fresh cold reconstruction. The local continuation verifies the genuine completed history through production startup snapshot, census and history predicates, using the complete actual physical roster with retired predecessors absent. This is a lower-layer replay check. The fresh-process restart remains required in the hosted Docker gate. The test performs no registry reset or historical-receipt rewrite.

The three-member, gapped-attempt/minimum-not-first aggregation control uses a minimal SQL projection and is a helper unit test. The genuine two-component continuation supplies the full-schema history proof. The041 immutability trigger stays enabled in migration tests: attempted receipt updates must fail and leave history unchanged. A foreign per-component creator state blocked by041 is source-reviewed rather than manufactured by removing that protection.

A completed mixed deployment's later use as a predecessor was traced through exact member receipts, policies, retirement043, handoff044 and completion/cold history guards. No additional uniform-attempt assumption was found. A second-generation journey retiring that mixed predecessor has not been executed locally.

## Final local verification

The final `unit2-eof-green-v1` packet passes12 main roster parents and63 distinct cases, plus the separate controller contract parent: **13 parents,64 distinct cases,92 raw pass records, zero failures or skips**. Both the exact isolated replacement child and the canonical runtime parent pass. Repeated subprocess records are not counted as distinct cases. This packet recompiles and verifies the final embedded migration bytes after removing an extra final blank line from each SQL mirror; no SQL rule changed.

The database roster covers047 fresh/mirror installation, byte-preserved historical042–045 upgrade and idempotence, minimum/roster/writer refusals, failed-upgrade restoration of the old trigger, existing046 writer/completion guards under047, and predecessor retirement. Ingress covers restored capacity graphs, valid/invalid cold prefixes, the complete runtime boundary parent and creator aggregation. The independent startup-snapshot and controller OpenAPI-contract tests also pass.

Pinned Go1.26.7 used `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`. Four packages were compiled with `go test -c`, exactly discovered with anchored `-test.list`, then executed with `-test.count=1` and `-test.v`. The replacement child also set the established registry/construction selectors. The existing90-second recovery bound was preserved. Read-only formatting for all changed Go bytes was empty across the two checkpoints. These final checks passed:

```text
go vet ./internal/database ./internal/generatedingress ./internal/generatedruntimestate
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
go run ./cmd/openapi-gen -check
```

Portable main test selectors:

```text
./internal/database:
  TestGeneratedRouteActivationMixedCreatorsMigrationFreshAndMirror
  TestGeneratedRouteActivationMixedCreatorsMigrationPreservesHistoricalChain
  TestGeneratedRouteActivationMixedCreatorsMigrationGuards
  TestGeneratedRouteActivationMixedCreatorsMigrationFailureRestoresTrigger
  TestGeneratedRouteActivationWritersMigrationActivationAndCompletionGuards
  TestGeneratedRoutePredecessorRetirementMigrationGuardsComponentAndAggregate
./internal/generatedingress:
  TestCandidateCapacityRestoredDebtRequiresExactPublishedTokenGraph
  TestGeneratedDomainColdRuntimeBoundary
  TestGeneratedDomainColdStartingCandidatePrefixHelperRefusesNoncontiguousDurableIdentity
  TestGeneratedDomainColdStartingCandidatePrefixPublicControls
  TestGeneratedRouteActivationCreatorMinimumHelperExactRoster
./internal/generatedruntimestate:
  TestGeneratedDomainStartupSnapshotTxReadsSortedPredecessorRetirementRowsIndependently
```

Run `TestOpenAPIContractMatchesRegisteredRoutes` from `internal/controller` so its existing relative contract-file lookup resolves correctly. The unchanged CI selector families include these parents; no workflow selection or time limit changed.

Final33-path manifest: `5704a3be2cf1535aad80c278c8dfd18678ca78bf45f4647c07df761faf5d43bc`; strictly ordinal composite: `69515bb4d5c90ae8428868a9d715bdc7f69dc9227b73e75d4deff1de8fac27a0`. Summaryv1: `266a6bfb97940dbc98a67a68f832c38cae61cdb609fad10e8a7e11898ae7aedd`. Independent18-stage native audit: `32437d93e11293d4afe70ff2602684208851e67aca305466c12b955cf93db5a9`. Both047 mirrors have SHA-256 `237c8c15ce98e2a73f59be2a185891b9d7f99aff79a6261d118986e981e3f2fc`.

Independent source, security and integration review passed against the final bytes. Earlier compile/fixture/replay failures and metadata-only corrections are preserved and excluded. One isolated launcher selected zero tests and is excluded; its corrected exact-selector run passed both required parent/child records using the same frozen binary. A separately generated security scan used a snapshot that later drifted; it is not final-freeze evidence. This remains a scoped local suite with a legacy binding and simulated Docker observations, not a full repository, Linux race, managed-authorizer or fresh-process Docker acceptance claim.

## Historical hosted observation

PR #259 at `8525d9cfc3eb089914157d4b6939724b6ccb355e`, hosted merge `c9666e0e8668ae2d9b0bdbbbc7760fe8a78eb347`, failed the `before_candidate_start` process-kill child at cold container attestation. Earlier `before_drain_stop`, `after_predecessor_retirement_before_handoff` and `before_build` children passed. The captured job-log SHA-256 is `f8549e0354975e4257ba308d9bf769937db7a6a1b9577f870191f255cf0b0015`.

The existing hosted matrix requires a nonempty serving predecessor before every boundary and submits an immutable replacement job. Its exact `before_candidate_start` child, assertions, required roster and time limits remain the actual Docker gate. These historical observations describe the older parent revision, not acceptance of this repair.

PR #260 reproduced this boundary in [process-kill job124984874831](https://github.com/tyhuang9/rig/actions/runs/41352277324/job/124984874831), tested merge `d409a92194c67c8c1acfe73ba7248c6c6f7e50a4`. The child failed at `cold_construction/cold_group_container_attestation`; decoded-log SHA-256 is `6984eaa8796cb9ef1ffb60d65be2555dc16912cdcbdb6ed6472c8ff2eae2b955`. This is source-bound hosted failure evidence, not a passing regression on the current base.

## Rollback and limits

Migration047 is additive and changes validation without rewriting immutable history. A failed upgrade is tested to restore the previous trigger and preserve data. Preserve the same containers, receipts, protected history and capacity charges when recovery refuses. The inherited [activation-recovery downgrade restrictions](m4-recovered-route-activation.md) still apply.

In particular, an older binary's uniform-creator reader may refuse an incomplete mixed-creator recovery. Once such history exists, a binary downgrade cannot be assumed safe merely because the stored history is unchanged.

External databases remain application-owned and configured through scoped runtime secrets. This change adds no managed database or Neon provisioning. Draft publication does not authorize a GitHub merge or deployment.
