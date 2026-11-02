# Cold recovery of previously accepted plans

Status: the local repair and public-constructor regression are verified on top of [PR #262](https://github.com/tyhuang9/rig/pull/262), head `e1bfb028bac3a9bbf98b99b75ff68c0a93c5ac98`. Current-head hosted Docker acceptance remains pending. M4 remains incomplete.

## Problem and contract

Deployment plans retain their immutable SQL acceptance status when a newer revision becomes current. The plan store reports the older revision as `superseded` in its derived state; its saved acceptance status remains `accepted`.

Cold reconstruction compared those two different representations. A deployment captured against an older accepted plan could therefore be refused after a later plan became current. Ordinary candidate validation already checks the persisted status against `RevisionAccepted` while separately allowing an exact protected revision whose derived state is either accepted or superseded.

The repair makes the cold comparison follow that existing contract. It changes one predicate and adds the existing deployment-plan package import. Exact application, revision ID/number, canonical digest, source, configuration, component roster, runtime state, lifecycle and history checks remain in place. It does not rewrite plans, approvals, receipts or deployment history, and it does not authorize a new approval for a superseded plan.

This changes no migration phase or outcome handling. In particular, reconstructing a deployment whose migration outcome is uncertain must not rerun that migration, invent a result or publish a route.

## Hosted trigger

[PR #262's migration approval/uncertainty job](https://github.com/tyhuang9/rig/actions/runs/41357365391/job/125000212742) failed at controller startup with `cold_construction/cold_group_reconstruction`. It tested merge `c902319410d77230f92f4743f9772fd0c7bc4eea`. The decoded log SHA-256 is `61d6564831edd500f2e944e616a687f93c03523f4b4b4d318c173e0253ce22e8`.

That journey completes v1, completes an approved/resumed v2, redeploys the original v1 release, then interrupts an approved/resumed v3 migration after its external operation but before recording the outcome. The previous three deployments have genuine completed history041–045. The durable v3 migration state remains `running` because its outcome was not recorded; there is no candidate or activation receipt. The test waits for the interrupted worker to finish before reopening the controller.

The rollback reuses the exact original v1 release, plan, configuration and secret-reference origins. v2 and v3 have their own later revisions. The ordinary deploy path in this journey uses revision-zero legacy lifecycle bindings and no lifecycle operations; the local fixture asserts that actual setup rather than substituting a different authorization mode.

## Local failing baseline

`TestGeneratedDomainColdMigrationUncertaintyHistoryReconstructsNoEffectSuccessor` constructs the four-deployment history through real durable job, runtime, candidate and history writers. Release/artifact rows and physical observations are fixture scaffolding. It does not execute Docker, a physical migration container or external database effects.

The immutable `baseline-v5` packet compiled and discovered the exact parent, then failed in1.65 seconds with attribution to `group_read`, role `v2_migrated`, and `construction_refused`. The test also called the public constructor afterward and received no domain, but its specific failure cause was not separately captured: the diagnostic prioritized the private-reader error. This is qualified reader-level failing evidence. Earlier compile and fixture setup failures are preserved and excluded.

The40-path manifest has SHA-256 `dd29aa0bf3c86ace348fa3d352526cd0cf062a3731f0a9896e79976a1e72f4a6`, with ordinal composite `a8511c5a6c0aad43f9e7def9466a11d920ec6b875a23aa06d1165c7f31e796eb`. All39 support/production copies match the committed base after line-ending normalization; only the regression test overlay differs. Corrected independent audit `caf735f283b63a494b8249d18666a9dbe8541364108bd253b7bf1f193e440908` binds those copies, the binary, compile/discovery receipts, exact selector and native failure. It corrects an earlier metadata inference that no public call occurred; the native evidence is unchanged. Target log SHA-256: `dd73f4d11a5ba36ad4e8e42ee1bd0eb1398b2773c1b264ad9d98abe63d5c77b1`.

## Final local verification

The final `final-v1` packet passes **five parents,15 distinct cases and22 raw pass records, with zero failures or skips**. Repeated subprocess parent records are not additional distinct cases. The new regression passes the actual public constructor, exact four-group history and current route, unchanged immutable snapshots, strict physical/image identity checks and the no-effect v3 manifest. It verifies no candidate, route or capacity action during construction. The saved migration outcome remains unchanged.

The finite compatibility roster also passed:

```text
internal/generatedingress:
  TestGeneratedDomainColdMigrationUncertaintyHistoryReconstructsNoEffectSuccessor
  TestGeneratedDomainColdRuntimeBoundary
  TestCandidateValidationRejectsConfigurationSourceAndRosterDrift
internal/deploymentplans:
  TestStorePersistsProtectedImmutableRevisionAndCASHead
  TestApproveMigrationRejectsSupersededRevision
```

Pinned Windows Go1.26.7 used `GOTOOLCHAIN=local`, `GOFLAGS=-mod=readonly -p=1 -buildvcs=false`, and `CGO_ENABLED=0`. Both packages were compiled, exactly discovered and executed from the frozen source. The new parent uses the existing registry/construction isolation selectors; it and the canonical runtime-boundary parent retain90-second bounds, with60 seconds for the other selections. These checks passed:

```text
gofmt -d internal/generatedingress/generated_domain_cold.go internal/generatedingress/generated_domain_cold_runtime_boundary_test.go
go vet ./internal/generatedingress ./internal/deploymentplans
go build -trimpath ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
go run ./cmd/openapi-gen -check
```

Final42-path manifest: `3c5081471b75bba680426394748c0cdf45bd7ba6aea9f29f1873a67fe2a9f41f`; ordinal composite: `db309200d5add200d412670f8105e6a30cc753de382b1dd0f77f2a508096bdc7`. Summary: `35a4cdbf388048d275a08e4da9dd1eaba11ae7e7322050df4f528e849541ff80`. Independent12-stage native audit: `0273226c5b9a3567e102620aebdd60bb2917d47f23169dfc96fd891b511bf34d`. Source, security and integration review passed against the final bytes.

Earlier image-attestation runs exposed fixture dispatch/format errors and an application-image ID colliding with the fixture gateway image. They are preserved and excluded. The corrected fixture checks three distinct authenticated application images and the complete physical roster before construction. No additional production change or guard relaxation was used to make those fixtures pass.

This is an isolated local constructor test with simulated physical observations, not a fresh operating-system-process Docker restart. No local full-repository suite, Linux race, network/database journey, controller refresh or deployment is claimed. The actual hosted migration approval/uncertainty journey must verify this head.

The unchanged `domain-cold-remainder` CI selector includes this new parent in both Linux race and Windows jobs. No workflow or time limit is changed.

## Scope and rollback

No schema migration, public API, database provisioning or secret storage behavior changes. External databases remain application-owned and configured through scoped runtime secrets; no managed database or Neon provisioning is added.

Reverting the predicate restores the previous refusal for superseded plans; it does not undo saved approvals or immutable history. Preserve the existing containers, protected state and accounting when reconstruction refuses. Publication is for draft CI review and does not authorize a merge or deployment.
