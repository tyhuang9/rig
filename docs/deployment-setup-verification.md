# Editable deployment setup verification

Purpose: make supported build and run settings explicit, editable, and durable without requiring the detector to rediscover a user-selected layout.

This change is based on `stack/m1-regression-integration` at `4baae47`, the latest generated-runtime integration work at implementation time. The persistent GitHub connector is a separate delivery branch based on the GitHub-defaults work. Both depend on their existing PR stacks; neither authorizes a merge or production rollout.

## Invariants and failure behavior

- Inspection reads source metadata without running repository commands. Manual setup requires a real inspected snapshot; a decoded or fabricated analysis object is insufficient.
- Review binds settings to source identity, resolved GitHub commit, structural fingerprint, and expected accepted revision. A changed source or concurrent save requires another review.
- Supported layouts are one server, one static site, or one of each. Each component owns its root, Node version, package manager, and commands. All explicit commands run from that component root inside the existing isolated containers.
- Empty installation/build commands skip those phases. Static output is relative to the component root and must be a real, contained directory after the build. Rig supplies its static start command.
- Inaccessible/unsafe source, invalid roots, incompatible Node engines, unsupported layouts, and unresolved migration evidence remain blocking. An unrelated Compose document does not execute for a generated setup.
- Accepted settings have field provenance and immutable revision pins. Reanalysis validates explicit settings without replacing them. Migration approval remains a separate administrator action; changed evidence requires review.
- New protected plan bundles use version 3; prior versions retain their canonical digests. The generated recipe uses compiler version 4. Existing release pins and recovery remain valid.

## Automated evidence

Dependency baseline: `pnpm --dir web install --frozen-lockfile`, production build, and 322 frontend tests passed on the unchanged runtime integration base. The initial older local documentation base also passed its baseline, but was replaced by the newer integration base before delivery.

The unchanged integration base and the feature both hit two timing-sensitive failures under the ordinary concurrent package Go run: `TestRunDashboardCommandWaitsForRepeatedRunnerCompletion` and `TestRealTCPDeadlineExemptionInvalidConnectAndKeepAlive`. Both passed individually on the unchanged base. No unrelated changes were made to those tests.

Feature checks:

- `go test -p 1 -count=1 -timeout=15m ./...` — passed, including both timing-sensitive tests.
- `go vet ./...` — passed.
- `go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe` — passed.
- `pwsh -NoProfile -File scripts/check-generation.ps1` — passed: generated OpenAPI artifacts, registered routes, and migration mirrors agree.
- `pwsh -NoProfile -File scripts/check-windows-controller.ps1` — passed; protection tests were confirmed to execute.
- `pnpm --dir web test` — 334 tests passed; TypeScript and production build passed. Vite retains its advisory about the existing large dashboard chunk.
- `pnpm --dir web e2e` — both Chromium scenarios passed (23.9 seconds), using the embedded controller for create/review/accept, missing application details, undetected source, numeric advanced settings, revision-two edits, reload persistence, and existing Compose behavior. A narrow-width check measures actual card and control bounds at 320px so clipping cannot hide overflow.
- `pwsh -NoProfile -File scripts/check-embedded.ps1` — passed; embedded files match the final production build.
- `gofmt -l` on changed Go files and `git diff --check` — no formatting errors.

Targeted tests cover undetected projects, absent scripts, malformed metadata, engine incompatibility, paired component roots, path containment, explicit skipped commands, static output validation, stale revisions/settings/GitHub commits, migration approval/evidence, restart persistence, subsequent deployments, and historical release recovery.

## Real container evidence

`TestLiveManualSetupRecipes` passed all four scenarios against Linux Docker through WSL (10.50 seconds): a Node application with installation/build skipped, a static application building into `public files`, checked-in `dist/client` output with a skipped build, and rejection of missing static output. Successful images also ran with a read-only filesystem, no network, non-root user, dropped capabilities, and bounded resources. Exact test-created images were removed by test cleanup.

`TestLiveManualGeneratedImageCompiler` passed through the production compiler, isolated Buildx builder, protected temporary storage, and real Docker image execution (28.28 seconds). It builds an undetected Node source with a component root containing a space, skipped installation, and a user-authored build command. The test verifies command redaction and owned-resource cleanup. Deterministic compiler tests separately reject changed revision pins, repository identity, runtime requirements, and source integrity before Docker is invoked.

Reproduction on a Linux Docker host with Buildx:

```sh
go test -tags live_docker -v -count=1 -timeout=10m ./internal/generatedimage -run '^TestLiveManualSetupRecipes$'
RIG_RUN_LIVE_GENERATED_RUNTIME=1 go test -v -count=1 -timeout=9m ./internal/generatedimage -run '^TestLiveManualGeneratedImageCompiler$'
```

The local host initially lacked Buildx. A task-specific copy of Docker Buildx v0.37.0 was downloaded from its official release and verified against its published SHA-256 checksum, without changing the user's Docker configuration. The test binary was cross-compiled from the checked-out Go source and executed in WSL. The production-compiler test accepts an optional absolute `RIG_LIVE_BUILDX_PLUGIN` path for this isolated test configuration. These checks do not replace the existing hosted blue/green lifecycle checks; the workflow requires both manual test functions to pass, rather than permitting undiscovered tests.

## Manual review steps

1. With a disposable Rig data root and generated runtime enabled, create an app from a repository containing only a Node server file. Enter its start command, review, and accept the setup. Verify the app draft and accepted revision survive a controller restart.
2. Set a child project root and distinct installation/build/start commands. Verify the commands run in that component directory. Empty the optional commands and confirm those phases are skipped.
3. Choose a static site with a custom output directory. Confirm there is no start-command input and Rig serves the built output. A missing or symlinked output must fail the build.
4. Add a static frontend and server using distinct roots. Verify their settings remain independent. Unsupported additional components must be rejected.
5. Edit commands, cause inspection or acceptance to fail, then retry. Edits must remain. Reanalysis must not replace them; only Reset to detected settings may do so.
6. Open existing application settings, save a new accepted revision, and deploy again. Verify the new release uses it while the prior release retains its original pin.
7. Change the source in another session before acceptance, or change migration evidence after approval. Verify review/approval is required again.
8. Repeat the connection-dependent paths with live GitHub authorization in a disposable account/repository. Confirm access removal, organization grants, and reconnect recovery. This external-consent check remains separate from controlled API/browser tests.

## Rollback and remaining promotion checks

Before rollout, take a consistent backup of the controller database, protected plan bundles, and their existing protection keys/credentials. Preserve runtime disablement controls. Stop the controller before restoring a matched pre-change data backup and binary; an older binary cannot read newly written version-3 plan bundles. Do not downgrade the binary alone after accepting new settings. Runtime schema migrations and their backup requirements remain owned by the existing generated-runtime stack.

Security, accessibility, and final integration review returned GO after the documented compiler, source-binding, draft-retention, concurrent-revision, error/focus, and narrow-layout fixes. Hosted Linux race, blue/green, and other infrastructure checks must pass before promotion. Live GitHub consent and repository-access grants were not exercised. The connector and setup branches share some contract and wizard files; combined-stack verification is recorded in the delivery PR separately from each branch's checks.
